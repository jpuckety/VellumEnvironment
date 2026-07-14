import json
import os
from decimal import Decimal

import boto3
from botocore.exceptions import ClientError

dynamodb = boto3.resource('dynamodb')
secretsmanager = boto3.client('secretsmanager')

TABLE_NAME = os.environ.get('TABLE_NAME')
table = dynamodb.Table(TABLE_NAME)


class DynamoJSONEncoder(json.JSONEncoder):
    """Serialize DynamoDB AttributeValue types that boto3 returns as Decimal/set."""

    def default(self, o):
        if isinstance(o, Decimal):
            # Ports and other whole numbers should stay integers for Go clients.
            if o == o.to_integral_value():
                return int(o)
            return float(o)
        if isinstance(o, set):
            return list(o)
        return super().default(o)

def handler(event, context):
    try:
        # Lambda Function URL can use rawPath (for v2) or path (for v1/rest)
        path = event.get('rawPath', event.get('path', ''))
        method = event.get('requestContext', {}).get('http', {}).get('method', event.get('httpMethod'))

        # Health check — no Google auth required (IAM still enforced at Function URL).
        # Keep this path free of optional imports so cold-start health stays reliable.
        if path.rstrip('/') == '/health' or path.strip('/') == 'health':
            if method and method != 'GET':
                return response(405, {'error': 'Method not allowed'})
            return response(200, {'status': 'ok'})

        # Lazy-import Google auth so /health works even if google-auth deps are missing.
        from google.oauth2 import id_token
        from google.auth.transport import requests as google_requests

        # 1. Authenticate with Google ID token.
        # Prefer X-Google-ID-Token: Function URL AWS_IAM auth puts SigV4 in Authorization,
        # which overwrites any Bearer token the client might set on that header.
        headers = {k.lower(): v for k, v in (event.get('headers') or {}).items()}
        token = extract_google_token(headers)
        if not token:
            return response(401, {
                'error': 'Missing or invalid Google ID token '
                         '(expected X-Google-ID-Token or Authorization: Bearer)'
            })

        google_client_id = os.environ.get('GOOGLE_CLIENT_ID')

        try:
            # Verify signature, expiry, and audience (GOOGLE_CLIENT_ID when set).
            id_info = id_token.verify_oauth2_token(token, google_requests.Request(), google_client_id)
            user_id = id_info['sub']
            # email = id_info['email']
        except Exception as e:
            return response(401, {'error': f'Invalid token: {str(e)}'})

        # 2. Parse Path
        parts = path.strip('/').split('/')
        if len(parts) != 3 or parts[0] != 'configs':
            return response(400, {'error': 'Invalid path format. Expected /configs/{applicationId}/{userId}'})
        
        app_id = parts[1]
        path_user_id = parts[2]
        
        # 3. Authorize - User can only access their own userId
        if path_user_id != user_id:
            return response(403, {'error': 'Forbidden: You can only access your own configuration'})
        
        if method == 'GET':
            return get_config(app_id, user_id)
        elif method == 'PUT':
            body_str = event.get('body', '{}')
            if event.get('isBase64Encoded', False):
                import base64
                body_str = base64.b64decode(body_str).decode('utf-8')
            body = json.loads(body_str)
            return put_config(app_id, user_id, body)
        elif method == 'DELETE':
            return delete_config(app_id, user_id)
        else:
            return response(405, {'error': 'Method not allowed'})

    except Exception as e:
        print(f"Error: {str(e)}")
        import traceback
        traceback.print_exc()
        return response(500, {'error': f'Internal server error: {str(e)}'})

def get_config(app_id, user_id):
    try:
        res = table.get_item(Key={'applicationId': app_id, 'userId': user_id})
        item = res.get('Item')
        if not item:
            return response(404, {'error': 'Config not found'})
        
        secret_arn = item.get('secretArn')
        password = None
        if secret_arn:
            try:
                secret_res = secretsmanager.get_secret_value(SecretId=secret_arn)
                secret_json = json.loads(secret_res['SecretString'])
                password = secret_json.get('password')
            except ClientError as e:
                print(f"Error fetching secret: {str(e)}")
        
        item['password'] = password
        
        return response(200, item)
    except ClientError as e:
        return response(500, {'error': str(e)})

def put_config(app_id, user_id, body):
    # body should contain imapHost, imapPort, imapUsername, and password
    password = body.pop('password', None)
    
    secret_id = f"emailmcp/imap/{app_id}/{user_id}"
    secret_arn = None
    
    if password:
        try:
            try:
                secretsmanager.put_secret_value(
                    SecretId=secret_id,
                    SecretString=json.dumps({'password': password}, cls=DynamoJSONEncoder)
                )
                secret_res = secretsmanager.describe_secret(SecretId=secret_id)
                secret_arn = secret_res['ARN']
            except secretsmanager.exceptions.ResourceNotFoundException:
                secret_res = secretsmanager.create_secret(
                    Name=secret_id,
                    SecretString=json.dumps({'password': password}, cls=DynamoJSONEncoder),
                    Description=f"IMAP password for {user_id} in {app_id}"
                )
                secret_arn = secret_res['ARN']
        except ClientError as e:
            return response(500, {'error': f"Failed to save secret: {str(e)}"})

    body['applicationId'] = app_id
    body['userId'] = user_id
    if secret_arn:
        body['secretArn'] = secret_arn
    
    try:
        table.put_item(Item=body)
        return response(200, {'message': 'Config saved successfully', 'userId': user_id, 'applicationId': app_id})
    except ClientError as e:
        return response(500, {'error': str(e)})

def delete_config(app_id, user_id):
    secret_id = f"emailmcp/imap/{app_id}/{user_id}"
    try:
        table.delete_item(Key={'applicationId': app_id, 'userId': user_id})
        try:
            secretsmanager.delete_secret(SecretId=secret_id, ForceDeleteWithoutRecovery=True)
        except (secretsmanager.exceptions.ResourceNotFoundException, ClientError):
            pass
        return response(200, {'message': 'Config deleted successfully'})
    except ClientError as e:
        return response(500, {'error': str(e)})

def extract_google_token(headers):
    """Return the Google ID token from request headers, or empty string.

    X-Google-ID-Token is preferred so it can coexist with SigV4 Authorization
    used by Lambda Function URL AWS_IAM auth.
    """
    token = (headers.get('x-google-id-token') or '').strip()
    if token:
        return token
    auth_header = (headers.get('authorization') or '').strip()
    if auth_header.lower().startswith('bearer '):
        return auth_header.split(' ', 1)[1].strip()
    return ''


def response(status_code, body):
    return {
        'statusCode': status_code,
        'headers': {'Content-Type': 'application/json'},
        'body': json.dumps(body, cls=DynamoJSONEncoder),
    }

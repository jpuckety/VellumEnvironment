import json
import os
from decimal import Decimal

import boto3
from botocore.exceptions import ClientError
from boto3.dynamodb.conditions import Key

dynamodb = boto3.resource('dynamodb')
secretsmanager = boto3.client('secretsmanager')

TABLE_NAME = os.environ.get('TABLE_NAME')
table = dynamodb.Table(TABLE_NAME)

# DynamoDB fields accepted from clients. Credentials and secret ARNs are never
# taken from the request body — they are managed server-side only.
ALLOWED_CONFIG_FIELDS = frozenset({
    'name',
    'imap_host',
    'imap_port',
    'imap_username',
    'imap_use_tls',
    'smtp_host',
    'smtp_port',
    'smtp_username',
    'smtp_use_tls',
    'from_address',
    'created_at',
    'updated_at',
})

# Keys that must never appear in list responses or DynamoDB items.
SECRET_RESPONSE_KEYS = frozenset({
    'password',
    'imap_password',
    'smtp_password',
    'secretArn',
    'secret_arn',
})


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

        google_client_id = (os.environ.get('GOOGLE_CLIENT_ID') or '').strip()
        if not google_client_id:
            # Fail closed: google-auth skips audience checks when audience is None/empty.
            print('ERROR: GOOGLE_CLIENT_ID is not set; refusing authenticated requests')
            return response(500, {'error': 'Server misconfigured'})

        try:
            # Verify signature, expiry, and audience (requires non-empty GOOGLE_CLIENT_ID).
            id_info = id_token.verify_oauth2_token(token, google_requests.Request(), google_client_id)
            user_id = id_info['sub']
            # email = id_info['email']
        except Exception as e:
            # Log details server-side only; never echo verification errors to clients.
            print(f'Invalid Google ID token: {e}')
            return response(401, {'error': 'Invalid token'})

        # 2. Parse Path
        parts = path.strip('/').split('/')
        if len(parts) < 3 or parts[0] != 'configs':
            return response(400, {'error': 'Invalid path format. Expected /configs/{applicationId}/{userId}[/{accountId}]'})
        
        app_id = parts[1]
        path_user_id = parts[2]
        account_id = parts[3] if len(parts) > 3 else None
        
        # 3. Authorize - User can only access their own userId
        if path_user_id != user_id:
            return response(403, {'error': 'Forbidden: You can only access your own configuration'})
        
        if method == 'GET':
            if account_id:
                return get_config(app_id, user_id, account_id)
            else:
                return list_configs(app_id, user_id)
        elif method == 'PUT':
            if not account_id:
                return response(400, {'error': 'Account ID required for PUT'})
            body_str = event.get('body', '{}')
            if event.get('isBase64Encoded', False):
                import base64
                body_str = base64.b64decode(body_str).decode('utf-8')
            body = json.loads(body_str)
            return put_config(app_id, user_id, account_id, body)
        elif method == 'DELETE':
            if not account_id:
                return response(400, {'error': 'Account ID required for DELETE'})
            return delete_config(app_id, user_id, account_id)
        else:
            return response(405, {'error': 'Method not allowed'})

    except Exception as e:
        print(f"Error: {str(e)}")
        import traceback
        traceback.print_exc()
        return response(500, {'error': 'Internal server error'})


def strip_secrets(item):
    """Return a shallow copy of item without credential material or secret ARNs."""
    if not item:
        return item
    return {k: v for k, v in item.items() if k not in SECRET_RESPONSE_KEYS}


def load_secret_passwords(secret_arn):
    """Load IMAP/SMTP passwords from a Secrets Manager document.

    Secret document shape:
      {
        "password": "<imap password>",       # legacy + primary IMAP key
        "imap_password": "<imap password>",  # optional explicit key
        "smtp_password": "<smtp password>"   # optional; falls back to IMAP password
      }
    """
    if not secret_arn:
        return None, None
    try:
        secret_res = secretsmanager.get_secret_value(SecretId=secret_arn)
        secret_json = json.loads(secret_res['SecretString'])
    except ClientError as e:
        print(f"Error fetching secret: {str(e)}")
        return None, None

    imap_password = (
        secret_json.get('imap_password')
        or secret_json.get('password')
    )
    smtp_password = secret_json.get('smtp_password')
    if not smtp_password:
        smtp_password = imap_password
    return imap_password, smtp_password


def list_configs(app_id, user_id):
    """List account configs without hydrating or returning secrets."""
    try:
        # Query all accounts for this user. SK format is "userId#accountId".
        res = table.query(
            KeyConditionExpression=Key('applicationId').eq(app_id) & Key('userId').begins_with(f"{user_id}#")
        )
        items = res.get('Items', [])
        
        # Also check for legacy single-account config where SK is just "userId"
        legacy_res = table.get_item(Key={'applicationId': app_id, 'userId': user_id})
        if legacy_res.get('Item'):
            items.append(legacy_res.get('Item'))

        # Never fetch Secrets Manager or echo passwords/secret ARNs in list responses.
        safe_items = [strip_secrets(item) for item in items]
        return response(200, safe_items)
    except ClientError as e:
        print(f"list_configs error: {e}")
        return response(500, {'error': 'Internal server error'})


def get_config(app_id, user_id, account_id):
    try:
        sk = f"{user_id}#{account_id}"
        # Fallback for legacy ID if account_id is some special value or we just want to be safe
        res = table.get_item(Key={'applicationId': app_id, 'userId': sk})
        item = res.get('Item')
        
        if not item and account_id == 'default':
            # Try legacy
            res = table.get_item(Key={'applicationId': app_id, 'userId': user_id})
            item = res.get('Item')

        if not item:
            return response(404, {'error': 'Config not found'})
        
        secret_arn = item.get('secretArn')
        imap_password, smtp_password = load_secret_passwords(secret_arn)

        # Return a client-safe base item, then attach passwords only for single-get
        # (EmailMCP needs them to dial IMAP/SMTP). secretArn is not echoed.
        out = strip_secrets(item)
        out['password'] = imap_password
        out['smtp_password'] = smtp_password
        
        return response(200, out)
    except ClientError as e:
        print(f"get_config error: {e}")
        return response(500, {'error': 'Internal server error'})


def put_config(app_id, user_id, account_id, body):
    """Store non-secret config in DynamoDB; IMAP+SMTP passwords in one secret document."""
    if not isinstance(body, dict):
        return response(400, {'error': 'body must be a JSON object'})

    # Extract credentials; never persist them in DynamoDB.
    password = body.pop('password', None)
    if password is None:
        password = body.pop('imap_password', None)
    smtp_password = body.pop('smtp_password', None)
    # Ignore any client-supplied secret handles / credential aliases.
    body.pop('secretArn', None)
    body.pop('secret_arn', None)

    sk = f"{user_id}#{account_id}"
    secret_id = f"emailmcp/imap/{app_id}/{user_id}/{account_id}"
    secret_arn = None

    if password:
        # Single secret document holds both passwords.
        secret_doc = {
            'password': password,
            'imap_password': password,
            'smtp_password': smtp_password if smtp_password else password,
        }
        try:
            try:
                secretsmanager.put_secret_value(
                    SecretId=secret_id,
                    SecretString=json.dumps(secret_doc, cls=DynamoJSONEncoder)
                )
                secret_res = secretsmanager.describe_secret(SecretId=secret_id)
                secret_arn = secret_res['ARN']
            except secretsmanager.exceptions.ResourceNotFoundException:
                secret_res = secretsmanager.create_secret(
                    Name=secret_id,
                    SecretString=json.dumps(secret_doc, cls=DynamoJSONEncoder),
                    Description=f"IMAP/SMTP passwords for {user_id}/{account_id} in {app_id}"
                )
                secret_arn = secret_res['ARN']
        except ClientError as e:
            print(f"Failed to save secret: {e}")
            return response(500, {'error': 'Internal server error'})
    else:
        # Password-less update: preserve existing secretArn from the stored item.
        try:
            existing = table.get_item(Key={'applicationId': app_id, 'userId': sk})
            if existing.get('Item') and existing['Item'].get('secretArn'):
                secret_arn = existing['Item']['secretArn']
            elif account_id == 'default':
                legacy = table.get_item(Key={'applicationId': app_id, 'userId': user_id})
                if legacy.get('Item') and legacy['Item'].get('secretArn'):
                    secret_arn = legacy['Item']['secretArn']
        except ClientError as e:
            print(f"put_config load existing error: {e}")
            return response(500, {'error': 'Internal server error'})

        # If only smtp_password is being rotated, merge into the existing secret.
        if smtp_password is not None and secret_arn:
            try:
                secret_res = secretsmanager.get_secret_value(SecretId=secret_arn)
                secret_doc = json.loads(secret_res['SecretString'])
                secret_doc['smtp_password'] = smtp_password
                if 'password' not in secret_doc and 'imap_password' in secret_doc:
                    secret_doc['password'] = secret_doc['imap_password']
                secretsmanager.put_secret_value(
                    SecretId=secret_id,
                    SecretString=json.dumps(secret_doc, cls=DynamoJSONEncoder)
                )
            except ClientError as e:
                print(f"Failed to update smtp password: {e}")
                return response(500, {'error': 'Internal server error'})

    # Allowlist non-secret fields only — never take secretArn or passwords from the client.
    item = {k: v for k, v in body.items() if k in ALLOWED_CONFIG_FIELDS}
    item['applicationId'] = app_id
    item['userId'] = sk
    item['id'] = account_id
    if secret_arn:
        item['secretArn'] = secret_arn

    try:
        table.put_item(Item=item)
        return response(200, {
            'message': 'Config saved successfully',
            'userId': user_id,
            'accountId': account_id,
            'applicationId': app_id,
        })
    except ClientError as e:
        print(f"put_config dynamodb error: {e}")
        return response(500, {'error': 'Internal server error'})


def delete_config(app_id, user_id, account_id):
    sk = f"{user_id}#{account_id}"
    secret_id = f"emailmcp/imap/{app_id}/{user_id}/{account_id}"
    try:
        table.delete_item(Key={'applicationId': app_id, 'userId': sk})
        try:
            secretsmanager.delete_secret(SecretId=secret_id, ForceDeleteWithoutRecovery=True)
        except (secretsmanager.exceptions.ResourceNotFoundException, ClientError):
            pass
        return response(200, {'message': 'Config deleted successfully'})
    except ClientError as e:
        print(f"delete_config error: {e}")
        return response(500, {'error': 'Internal server error'})


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

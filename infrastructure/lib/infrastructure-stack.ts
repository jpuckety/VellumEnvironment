import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as kms from 'aws-cdk-lib/aws-kms';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as cloudtrail from 'aws-cdk-lib/aws-cloudtrail';
import * as ecr from 'aws-cdk-lib/aws-ecr';
import * as acm from 'aws-cdk-lib/aws-certificatemanager';
import * as path from 'path';

export class InfrastructureStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    // 1. KMS Key for encryption
    const kmsKey = new kms.Key(this, 'EmailMCPKey', {
      enableKeyRotation: true,
      description: 'KMS key for EmailMCP DynamoDB and Secrets Manager',
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    // 2. DynamoDB Table
    const table = new dynamodb.Table(this, 'EmailMCPUserConfigs', {
      partitionKey: { name: 'applicationId', type: dynamodb.AttributeType.STRING },
      sortKey: { name: 'userId', type: dynamodb.AttributeType.STRING },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      encryption: dynamodb.TableEncryption.CUSTOMER_MANAGED,
      encryptionKey: kmsKey,
      pointInTimeRecovery: true,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    // 3. Lambda Function for Config API
    const googleClientId = this.node.tryGetContext('googleClientId');

    const configApiLambda = new lambda.Function(this, 'EmailMCPConfigApi', {
      runtime: lambda.Runtime.PYTHON_3_12,
      handler: 'main.handler',
      code: lambda.Code.fromAsset(path.join(__dirname, '../../emailmcp-config-api/dist')),
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
      environment: {
        TABLE_NAME: table.tableName,
        KMS_KEY_ARN: kmsKey.keyArn,
        GOOGLE_CLIENT_ID: googleClientId || '',
      },
    });

    // 4. Permissions
    table.grantReadWriteData(configApiLambda);
    kmsKey.grantEncryptDecrypt(configApiLambda);
    
    // Grant permissions to Secrets Manager
    configApiLambda.addToRolePolicy(new iam.PolicyStatement({
      actions: [
        'secretsmanager:CreateSecret',
        'secretsmanager:GetSecretValue',
        'secretsmanager:PutSecretValue',
        'secretsmanager:DeleteSecret',
        'secretsmanager:TagResource',
        'secretsmanager:DescribeSecret',
      ],
      resources: [`arn:aws:secretsmanager:${this.region}:${this.account}:secret:emailmcp/imap/*`],
    }));

    // 5. Lambda Function URL
    const functionUrl = configApiLambda.addFunctionUrl({
      authType: lambda.FunctionUrlAuthType.AWS_IAM,
      cors: {
        allowedOrigins: ['*'],
        allowedMethods: [lambda.HttpMethod.ALL],
        allowedHeaders: ['Authorization', 'Content-Type'],
      },
    });

    // 6. IRSA Role for EKS (Preferred)
    const eksOidcProvider = this.node.tryGetContext('eksOidcProvider');
    const eksOidcProviderArn = this.node.tryGetContext('eksOidcProviderArn');

    if (eksOidcProvider || eksOidcProviderArn) {
      let providerArn: string;
      let oidcProviderClean: string;

      if (eksOidcProviderArn) {
        providerArn = eksOidcProviderArn;
        // Extract provider name from ARN (everything after 'oidc-provider/')
        oidcProviderClean = eksOidcProviderArn.split('oidc-provider/')[1];
      } else {
        oidcProviderClean = eksOidcProvider.replace('https://', '');
        providerArn = `arn:aws:iam::${this.account}:oidc-provider/${oidcProviderClean}`;
      }

      const irsaRole = new iam.Role(this, 'EmailMCPIRSARole', {
        assumedBy: new iam.FederatedPrincipal(
          providerArn,
          {
            "StringEquals": {
              [`${oidcProviderClean}:sub`]: "system:serviceaccount:emailmcp:emailmcp",
              [`${oidcProviderClean}:aud`]: "sts.amazonaws.com",
            },
          },
          "sts:AssumeRoleWithWebIdentity"
        ),
      });
      functionUrl.grantInvokeUrl(irsaRole);
      
      new cdk.CfnOutput(this, 'IrsaRoleArn', {
        value: irsaRole.roleArn,
      });
    }

    // 8. Audit Logging (CloudTrail)
    const trail = new cloudtrail.Trail(this, 'EmailMCPAuditTrail', {
      managementEvents: cloudtrail.ReadWriteType.ALL,
    });
    // Log data events for the Lambda
    trail.addLambdaEventSelector([configApiLambda]);

    new cdk.CfnOutput(this, 'ConfigApiUrl', {
      value: functionUrl.url,
    });

    // 9. ECR Repository for the Go MCP Server
    const repository = new ecr.Repository(this, 'EmailMCPRepository', {
      repositoryName: 'emailmcp',
      removalPolicy: cdk.RemovalPolicy.DESTROY,
      autoDeleteImages: true,
    });

    new cdk.CfnOutput(this, 'EcrRepositoryUri', {
      value: repository.repositoryUri,
    });

    // 10. Certificate for EKS Ingress
    const certificate = new acm.Certificate(this, 'EmailMCPCertificate', {
      domainName: 'emailmcp.ecg.co',
      validation: acm.CertificateValidation.fromDns(),
    });

    new cdk.CfnOutput(this, 'CertificateArn', {
      value: certificate.certificateArn,
    });
  }
}

import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as kms from 'aws-cdk-lib/aws-kms';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as cloudtrail from 'aws-cdk-lib/aws-cloudtrail';
import * as ecr from 'aws-cdk-lib/aws-ecr';
import * as acm from 'aws-cdk-lib/aws-certificatemanager';
import * as ssm from 'aws-cdk-lib/aws-ssm';

export class InfrastructureStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    // 1. KMS Key for encryption
    const kmsKey = new kms.Key(this, 'EmailMCPKey', {
      enableKeyRotation: true,
      description: 'KMS key for EmailMCP DynamoDB tables',
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    // 2. DynamoDB Table (per-user email account configurations).
    // The Go MCP server reads/writes this table directly via IRSA (the former
    // Python Config API Lambda has been removed). IMAP/SMTP passwords are
    // stored as attributes here, encrypted at rest with the shared KMS key.
    const table = new dynamodb.Table(this, 'EmailMCPUserConfigs', {
      partitionKey: { name: 'applicationId', type: dynamodb.AttributeType.STRING },
      sortKey: { name: 'userId', type: dynamodb.AttributeType.STRING },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      encryption: dynamodb.TableEncryption.CUSTOMER_MANAGED,
      encryptionKey: kmsKey,
      pointInTimeRecovery: true,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    // 2b. DynamoDB Session Table (EmailMCP OAuth sessions + registered clients)
    // The Go MCP server persists opaque access/refresh token sessions and
    // Dynamic Client Registrations here so they survive restarts and span
    // replicas. Items are namespaced by the "pk" partition key; a GSI on
    // "refreshToken" supports the refresh_token grant, and a "ttl" attribute
    // expires stale sessions/clients automatically.
    const sessionTable = new dynamodb.Table(this, 'EmailMCPSessions', {
      partitionKey: { name: 'pk', type: dynamodb.AttributeType.STRING },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      encryption: dynamodb.TableEncryption.CUSTOMER_MANAGED,
      encryptionKey: kmsKey,
      pointInTimeRecovery: true,
      timeToLiveAttribute: 'ttl',
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    sessionTable.addGlobalSecondaryIndex({
      indexName: 'refresh-index',
      partitionKey: { name: 'refreshToken', type: dynamodb.AttributeType.STRING },
      projectionType: dynamodb.ProjectionType.ALL,
    });

    // 3. IRSA Role for EKS (Preferred)
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

      // Grant permission to read the SSM parameters (DynamoDB table names).
      irsaRole.addToPolicy(new iam.PolicyStatement({
        actions: ['ssm:GetParameter'],
        resources: [
          `arn:aws:ssm:${this.region}:${this.account}:parameter/emailmcp/session-table/name`,
          `arn:aws:ssm:${this.region}:${this.account}:parameter/emailmcp/user-config-table/name`,
        ],
      }));

      // The Go MCP server reads/writes OAuth sessions AND per-user email account
      // configurations directly in DynamoDB (both encrypted with the shared KMS
      // key) via IRSA. The former Config API Lambda has been removed.
      sessionTable.grantReadWriteData(irsaRole);
      table.grantReadWriteData(irsaRole);
      kmsKey.grantEncryptDecrypt(irsaRole);

      new cdk.CfnOutput(this, 'IrsaRoleArn', {
        value: irsaRole.roleArn,
      });
    }

    // 8. Audit Logging (CloudTrail)
    new cloudtrail.Trail(this, 'EmailMCPAuditTrail', {
      managementEvents: cloudtrail.ReadWriteType.ALL,
    });

    // 11. SSM Parameter for the user config table name (consumed by the Go MCP server)
    new ssm.StringParameter(this, 'UserConfigTableNameParam', {
      parameterName: '/emailmcp/user-config-table/name',
      stringValue: table.tableName,
      description: 'The DynamoDB table name for EmailMCP per-user email account configurations',
    });

    new cdk.CfnOutput(this, 'UserConfigTableName', {
      value: table.tableName,
    });

    // 11b. SSM Parameter for the session table name (consumed by the Go MCP server)
    new ssm.StringParameter(this, 'SessionTableNameParam', {
      parameterName: '/emailmcp/session-table/name',
      stringValue: sessionTable.tableName,
      description: 'The DynamoDB table name for EmailMCP OAuth sessions',
    });

    new cdk.CfnOutput(this, 'SessionTableName', {
      value: sessionTable.tableName,
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

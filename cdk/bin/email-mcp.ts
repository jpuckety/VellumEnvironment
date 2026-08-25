#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { EmailMcpStack } from '../lib/email-mcp-stack';

const app = new cdk.App();

new EmailMcpStack(app, 'EmailMcpStack', {
  env: {
    account: process.env.CDK_DEFAULT_ACCOUNT,
    region: process.env.CDK_DEFAULT_REGION,
  },
  hostname: app.node.tryGetContext('hostname') ?? process.env.PUBLIC_HOSTNAME,
  certificateArn: app.node.tryGetContext('certificateArn') ?? process.env.CERTIFICATE_ARN,
  allowInsecureHttp:
    String(app.node.tryGetContext('allowInsecureHttp') ?? process.env.ALLOW_INSECURE_HTTP ?? 'false') === 'true',
  hostedZoneName: app.node.tryGetContext('hostedZoneName') ?? process.env.HOSTED_ZONE_NAME,
  hostedZoneId: app.node.tryGetContext('hostedZoneId') ?? process.env.HOSTED_ZONE_ID,
  pipelineAccount: app.node.tryGetContext('pipelineAccount') ?? process.env.PIPELINE_ACCOUNT,
  imageUri: app.node.tryGetContext('imageUri') ?? process.env.IMAGE_URI,
});

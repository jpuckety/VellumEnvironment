# email-mcp CDK

Env stack (`EmailMcpStack`) for ECS Fargate + CodeDeploy blue/green. MCPCICD
deploys this into Dev and Prod; do not `cdk deploy` from a laptop except for
break-glass.

```bash
./run.sh synth
# or
cd cdk && npm test
```

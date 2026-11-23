# Prerequisites

This page lists everything you need before you install Harbormaster on AWS. It is for the engineer who plans the install.

## Tools on your workstation

| Tool | Version | Used for |
| --- | --- | --- |
| Docker | Any recent version | Building and pushing the image |
| AWS CLI | v2, with a named profile for the account that runs Harbormaster | Logging in to ECR, reading logs |
| Terraform | 1.5 or newer | Applying the modules |

You do not need Go or Node to build the image. The Dockerfile builds the web app and the binary inside Docker. You need them only to run Harbormaster from source (see [Quickstart: run it locally](../getting-started/quickstart-local.md)).

## Terraform providers

The `harbormaster` module needs:

- AWS provider 5.82 or newer, below 7.0.
- `random` provider 3.5 or newer.

You need a Terraform runner that can apply the modules in the Harbormaster account and, for the access module, in each account you connect. Run plan and apply with your usual Terraform workflow.

## AWS account that runs Harbormaster

- **A VPC** with at least two private subnets in different AZs. You also need public subnets unless you run an internal load balancer (`internal = true`, recommended).
- **Outbound access** from the private subnets: a NAT gateway, or VPC endpoints for `ecr.api`, `ecr.dkr`, `logs`, `secretsmanager` and `sts`, plus the S3 gateway endpoint for ECR layers. If you use image scanning, the scanner also reaches ECR, S3 and STS on 443.
- **An ACM certificate** in the same region, for the HTTPS listener.
- **A hostname** for `base_url`, and a way to create a DNS record that points at the load balancer.
- **An ECR repository** for the Harbormaster image. The image must be pinned to a tag; `:latest` is rejected. Its architecture must match the module's `cpu_architecture`.
- **Network reach to the app.** With an internal load balancer, your users need a path to it, such as a VPN or office network. List those ranges in `allowed_cidrs`.

The module creates the ECS cluster and service, the RDS PostgreSQL instance, the Secrets Manager secrets, the log group, security groups and IAM roles. You do not create these yourself.

## Each AWS account you want to manage

- Permission to create IAM roles there (to apply the `harbormaster-access` module).
- For the terminal: services with ECS Exec enabled, and a task role with the `ssmmessages` permissions that ECS Exec needs.
- For service metrics: Container Insights enabled on the clusters you want charts for.

<!-- TODO(docs): verify whether Container Insights is required for every metrics chart or only for CPU and memory -->

## Optional components

| Feature | Needs |
| --- | --- |
| [Split services](split-services.md) | A Valkey or Redis store, created by the module or your own |
| [Single sign-on](single-sign-on.md) | A SAML 2.0 identity provider |
| Image scanning | The scanner image, pushed to ECR (see [Build the image](build-image.md)) |
| Email notifications | An SMTP server |
| [MCP gateway](mcp-gateway.md) | The gateway image, public subnets, a public hostname and certificate |

## Next steps

- [Build the image](build-image.md)
- [Deploy with Terraform](deploy-with-terraform.md)

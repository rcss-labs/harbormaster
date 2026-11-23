# Quickstart: deploy on AWS

This page is the shortest real path to a working Harbormaster on AWS: build and push the image, apply the Terraform module, sign in, and connect one account. It links to the install pages for depth. It is for platform engineers with an AWS account and a Terraform workflow.

## Before you begin

Have these ready. The full list is in [Prerequisites](../install/prerequisites.md).

- Docker and AWS CLI v2, with a profile for the account that will run Harbormaster.
- A way to run Terraform plan and apply for that account.
- A VPC with two private subnets in different AZs, an ACM certificate in the same region, and a hostname for Harbormaster.
- Your VPN or office CIDR ranges.

## Steps

1. **Build and push the image.** Create an ECR repository if you do not have one, then build, tag and push. The tag must be a specific version, never `:latest`.

    ```sh
    make image VERSION=v1.0.0
    aws ecr get-login-password --profile <profile> --region <region> \
      | docker login --username AWS --password-stdin <account-id>.dkr.ecr.<region>.amazonaws.com
    docker tag harbormaster:v1.0.0 <account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster:v1.0.0
    docker push <account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster:v1.0.0
    ```

    Details, including Graviton (`ARCH=arm64`), are in [Build the image](../install/build-image.md).

2. **Write the Terraform.** Call the module, pinned to a release tag.

    ```hcl
    module "harbormaster" {
      source = "github.com/rcss-labs/harbormaster//terraform/modules/harbormaster?ref=v1.0.0"

      vpc_id             = "vpc-0123456789abcdef0"
      private_subnet_ids = ["subnet-0123456789abcdef0", "subnet-0123456789abcdef1"]
      certificate_arn    = "arn:aws:acm:<region>:<account-id>:certificate/<certificate-id>"
      image              = "<account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster:v1.0.0"
      base_url           = "https://harbormaster.example.com"
      internal           = true
      allowed_cidrs      = ["10.0.0.0/8"] # your VPN or office ranges
    }

    output "task_role_arn" {
      value = module.harbormaster.task_role_arn
    }

    output "alb_dns_name" {
      value = module.harbormaster.alb_dns_name
    }
    ```

    See [Deploy with Terraform](../install/deploy-with-terraform.md) for every input.

3. **Run plan and apply** with your usual Terraform workflow. Review the plan before you apply.

4. **Point DNS at the load balancer.** Create a record for the `base_url` hostname that points at the `alb_dns_name` output (an alias record, using the `alb_zone_id` output, if you use Route 53).

5. **Create the first admin.** On the first start, with no users, Harbormaster logs a one-time setup token. Read it from the log group, then open the app and enter it. See [First run](../install/first-run.md) for the other options.

    ```sh
    aws logs tail /ecs/harbormaster/harbormaster-server --since 15m --profile <profile> --region <region>
    ```

6. **Connect one AWS account.** In the web app open **AWS accounts > Connect account** and copy the external ID. In the target account, apply the `harbormaster-access` module with the `task_role_arn` output and that external ID. Enter the module's `role_arn` output in the web app and press **Test and connect**. See [Connect AWS accounts](../install/connect-aws-accounts.md).

## Check it worked

- <https://harbormaster.example.com/healthz> answers `ok` (from inside your allowed network).
- You can sign in as the admin you created.
- **AWS accounts** shows the account as connected, and **Clusters** lists its clusters after the first poll.

## Next steps

- [Single sign-on](../install/single-sign-on.md)
- [Split services](../install/split-services.md), if you need to scale roles separately
- [Users, tags and permissions](../guides/users-tags-permissions.md)
- [Hardening](../security/hardening.md)

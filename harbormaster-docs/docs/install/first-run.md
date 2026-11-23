# First run

This page covers the first start of a new Harbormaster: creating the first global admin, signing in, and connecting your first AWS account. It is for the engineer who just applied the module.

## Before you begin

- The module is applied and the service is running (see [Deploy with Terraform](deploy-with-terraform.md)).
- DNS for `base_url` points at the load balancer, and you can reach it from an allowed network.

## Create the first global admin

On the first start, with no users in the database, Harbormaster needs a first global admin. Choose one of three ways.

### Setup token

The server logs a one-time setup token. Read it from the log group `/ecs/harbormaster/harbormaster-server`:

```sh
aws logs tail /ecs/harbormaster/harbormaster-server --since 15m --profile <profile> --region <region>
```

Open the app, enter the token, and choose a username and password. The token is regenerated on each start until a user exists.

Usernames use 2 to 32 characters: lowercase letters, digits, dot, dash or underscore, starting with a letter. Passwords need at least 12 characters.

### Bootstrap secret

Set `bootstrap_admin_password_secret_arn` in the module (it becomes `HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN`) to a Secrets Manager secret that holds the initial password for the `admin` user. The server then creates `admin` from it and prints no setup token. The secret value is never logged.

If the secret is encrypted with your own KMS key (`db_kms_key_id`), also grant `kms:Decrypt` on the key to the task role.

### CLI

`harbormaster create-admin --username <name>` creates an administrator directly. Run it with the same database settings as the server. It creates a break-glass admin, so the account can sign in with its password even when single sign-on is on.

```sh
harbormaster create-admin --username <name>
```

Without `--password-stdin` it generates a temporary password, prints it once, and requires a change at first sign-in. To set the password yourself, pass it on standard input:

```sh
harbormaster create-admin --username <name> --password-stdin
```

The command exits 3 if the user already exists, and 2 on a usage error.

<!-- TODO(docs): document how to run create-admin inside the ECS task (for example with ECS Exec) once a supported procedure exists; the image has no shell -->

## Sign in

Open `base_url` in your browser and sign in with the admin account. If you used a temporary password, Harbormaster asks you to change it.

## Connect your first account

1. Open **AWS accounts > Connect account**.
2. Copy the generated external ID.
3. Apply the `harbormaster-access` module in the target account with the external ID and Harbormaster's task role ARN.
4. Enter the module's `role_arn` output and press **Test and connect**.

The full procedure is in [Connect AWS accounts](connect-aws-accounts.md).

## Check it worked

- You can sign in, and **AWS accounts** lists your account as connected.
- After the first poll, **Clusters** shows the account's clusters.

## Next steps

- [Single sign-on](single-sign-on.md)
- [Users, tags and permissions](../guides/users-tags-permissions.md): create tags and give people access. A new user has no permissions until an admin grants them.
- [Navigating the web app](../guides/navigating.md)

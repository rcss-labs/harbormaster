# Roll back a service

This guide shows how to return a service to an earlier task definition revision: from the Revisions table, from the Timeline after a failure, and from a rollout in progress. It is for engineers with Deploy on the service.

## Before you begin

- You need **Deploy** on the service's tag.
- The service must use the ECS rolling deployment controller. For CodeDeploy, external or ECS native blue/green services, rollback is disabled and a banner says why.
- A rollback is an ordinary deploy of an older revision of the same family. It changes only the task definition. It does not undo changes to desired count, auto scaling, ECS Exec or rollout settings, and it does not undo anything outside the task definition, such as a database migration.
- ECS keeps the old revision only while it is active. A revision that someone deregistered cannot be deployed.

See [Restrictions](#restrictions) for everything that can stop a rollback.

## Roll back from the Revisions table

1. Open the service and choose the **Task definition** tab.
2. In **Revisions**, find the revision you want. Its **Status** is **Previous**. Check the image tag, size, who registered it and when.
3. Click **Roll back to this**. The Deploy tab opens with that revision loaded. The Pending changes box says `:N is loaded from the revisions list`.
4. Click **Review...**. The review shows `Task definition: :current -> :N` with a diff of the two definitions. Check it.
5. Optionally enter a **Reason**. It is saved with the change and shown in the History tab and the Timeline.
6. Click **Deploy :N**.

On a phone, use the **...** button on the revision row and choose **Roll back to this**.

A revision that was never deployed shows **Deploy** instead of **Roll back to this**. The flow is the same.

## Roll back from the Timeline

When tasks start failing soon after a change, the Timeline names the change as the likely trigger. See [Timeline](timeline.md).

1. Open the service and choose the **Timeline** tab. If tasks have been failing, a banner at the top reads, for example, `Tasks began failing 4 minutes after revision 43.`
2. Click **Show the change** to select the trigger on the timeline and read its details.
3. In the **Details** panel, click **Roll back to revision N**.
4. The Deploy tab opens with revision N loaded and the review already open. Check the diff, add a reason, and click **Deploy :N**.

The review opens for you, but nothing is applied until you click the button.

The button appears only when Harbormaster can say a rollback is safe to offer: the trigger was a deploy that moved the service forward, that revision is still the one deployed, and no other rollout has happened since. If you lack Deploy, the button is disabled and says `You need Deploy on <tag>`. If the failures began a long time ago, or Harbormaster holds too many recent failures to find the cause, no rollback is offered, and the banner says so. In that case use the Revisions table.

If someone already rolled back, the banner reads `Rollback to revision N started by <user>` instead.

## Roll back a rollout in progress

While a new revision is rolling out, or after it failed, the Deploy tab shows a **Rollout progress** panel. If an older revision still has running tasks, the panel has a **Roll back to :N** button.

1. Open the **Deploy** tab.
2. Click **Roll back to :N**.
3. Confirm `Roll back <service> to :N?` The dialog says it stops the rollout of the new revision and returns the service to :N.

This is the one deploy that skips the review dialog. It interrupts the running rollout, which is why it is allowed only toward an older revision. ECS replaces the new tasks with tasks of the old revision.

If the circuit breaker and **Roll back on failure** are on, ECS already rolls back a failing deployment by itself. See [Deploy a change](deploy-a-change.md#service).

## Restrictions

| Rule | What you see |
|---|---|
| Deploy is required. | The button is disabled with `You need Deploy on <tag>`. |
| Only the rolling deployment controller is supported. | The banner `This service is deployed by <controller>. Harbormaster shows it but can't deploy it yet.` |
| A rollout is running. | The Revisions table buttons are disabled with `A rollout is already running`. Only the in-progress **Roll back to :N** works, and only toward an older revision. A forward deploy is refused. |
| The target is already deployed. | `Revision :N is already deployed. Use Restart to replace its tasks.` |
| The target must be an active revision of this service's family. | `Revision :N is not active and cannot be deployed`, or `<family>:N is not a revision of this service's task definition`. |
| The target sends logs somewhere the running revision does not. A user who is not a global admin cannot roll to it. | `Changing where a container's logs go needs a global admin`. Ask a global admin to deploy it. |
| The image scan gate is on, and Harbormaster has no record of the target revision running on this service, and its image differs from the running one. | The dialog pauses and offers **Deploy anyway**. A revision that ran before is never gated, so ordinary rollbacks stay instant. See [Image security](image-security.md). |
| The service changed since you loaded the page. | `The service moved from :A to :B while you were editing.` Check the diff and apply again. |

## Check it worked

- The **Rollout progress** panel shows `:N -> ...` and ends at **Completed**.
- The service header shows `family:N`, and the Revisions table marks that revision **Deployed**.
- The History tab has a line `Rolled back family:N` with your name and reason, and the Timeline shows the change.

## After a rollback

- Terraform will see the older revision as drift. If Terraform manages `task_definition`, add it to `ignore_changes`, or apply a fix through Terraform. See [Deploy a change](deploy-a-change.md#terraform-and-harbormaster).
- Fix the bad revision before the next deploy. Rolling back does not remove it, and the pending-revision banner may offer it again.

## Next steps

- [Deploy a change](deploy-a-change.md)
- [Timeline](timeline.md)
- [Logs](logs.md): read the failing tasks' output

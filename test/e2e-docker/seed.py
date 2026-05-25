"""Bootstrap Plane CE for the plane-tug e2e test.

Runs inside the plane-api container via:
    docker compose exec plane-api python /tmp/seed.py

Creates:
  - the singleton Instance row + admin role
  - an admin user with a known password
  - a Workspace ("plane-tug-ci")
  - a Project (identifier "PT")
  - an APIToken with a known value
  - a workspace-level Webhook pointing at http://plane-tug:8080/webhook
    with a known HMAC secret_key so plane-tug's PLANE_TUG_WEBHOOK_SECRET
    env var can be hard-coded in compose.yaml. Plane normally generates
    this on first save; overriding it post-create is fine.

Emits a single JSON line on stdout with everything the test program
needs. The surrounding shell script captures it.
"""
import json
import os
import sys
import uuid

import django

os.environ.setdefault("DJANGO_SETTINGS_MODULE", "plane.settings.production")
django.setup()

from django.contrib.auth.hashers import make_password
from django.utils import timezone

from plane.db.models import (
    APIToken,
    Project,
    ProjectIdentifier,
    ProjectMember,
    User,
    Webhook,
    Workspace,
    WorkspaceMember,
)
from plane.license.models import Instance, InstanceAdmin, InstanceEdition


ADMIN_EMAIL = "plane-tug-admin@plane-tug.test"
ADMIN_PASSWORD = "plane-tug-admin-pass-ci-only"
ADMIN_USERNAME = "plane-tug-admin"
WORKSPACE_NAME = "plane-tug-ci"
WORKSPACE_SLUG = "plane-tug-ci"
PROJECT_NAME = "plane-tug e2e"
PROJECT_IDENTIFIER = "PT"
TOKEN_LABEL = "plane-tug-e2e"
WEBHOOK_URL = "http://plane-tug:8080/webhook"
WEBHOOK_SECRET = "e2e-known-secret-not-random"


def ensure_instance() -> Instance:
    instance = Instance.objects.first()
    if instance is None:
        instance = Instance.objects.create(
            instance_name="Plane CE - plane-tug e2e",
            instance_id=uuid.uuid4().hex[:24],
            current_version="v1.3.1",
            latest_version="v1.3.1",
            last_checked_at=timezone.now(),
            is_test=True,
            edition=InstanceEdition.PLANE_COMMUNITY.value,
        )
    if not instance.is_setup_done:
        instance.is_setup_done = True
        instance.save(update_fields=["is_setup_done"])
    return instance


def ensure_admin_user() -> User:
    user, created = User.objects.get_or_create(
        email=ADMIN_EMAIL,
        defaults={
            "username": ADMIN_USERNAME,
            "password": make_password(ADMIN_PASSWORD),
            "is_active": True,
            "is_email_verified": True,
            "first_name": "plane-tug",
            "last_name": "Admin",
            "display_name": ADMIN_USERNAME,
        },
    )
    if created:
        user.set_password(ADMIN_PASSWORD)
        user.is_password_autoset = False
        user.save()
    return user


def ensure_instance_admin(instance: Instance, user: User) -> None:
    InstanceAdmin.objects.get_or_create(
        instance=instance,
        user=user,
        defaults={"role": 20},
    )


def ensure_workspace(owner: User) -> Workspace:
    workspace, _ = Workspace.objects.get_or_create(
        slug=WORKSPACE_SLUG,
        defaults={
            "name": WORKSPACE_NAME,
            "owner": owner,
            "organization_size": "Just myself",
        },
    )
    WorkspaceMember.objects.get_or_create(
        workspace=workspace,
        member=owner,
        defaults={"role": 20},
    )
    return workspace


def ensure_project(workspace: Workspace, owner: User) -> Project:
    project, _ = Project.objects.get_or_create(
        workspace=workspace,
        identifier=PROJECT_IDENTIFIER,
        defaults={
            "name": PROJECT_NAME,
            "created_by": owner,
            "updated_by": owner,
            "network": 2,
        },
    )
    ProjectIdentifier.objects.get_or_create(
        workspace=workspace,
        name=PROJECT_IDENTIFIER,
        defaults={"project": project},
    )
    ProjectMember.objects.get_or_create(
        workspace=workspace,
        project=project,
        member=owner,
        defaults={"role": 20, "is_active": True},
    )
    return project


def ensure_default_states(project: Project, owner: User) -> None:
    from plane.db.models import State

    defaults = [
        ("Backlog", "backlog", "#5e6ad2"),
        ("Todo", "unstarted", "#3f76ff"),
        ("In Progress", "started", "#f59e0b"),
        ("Done", "completed", "#22c55e"),
        ("Cancelled", "cancelled", "#ef4444"),
    ]
    for name, group, color in defaults:
        State.objects.get_or_create(
            workspace=project.workspace,
            project=project,
            name=name,
            defaults={
                "group": group,
                "color": color,
                "default": name == "Backlog",
                "created_by": owner,
                "updated_by": owner,
            },
        )


def ensure_api_token(user: User, workspace: Workspace) -> APIToken:
    desired_token = os.environ.get("PLANE_TUG_PLANE_API_KEY", "").strip()
    existing = APIToken.objects.filter(
        user=user, workspace=workspace, label=TOKEN_LABEL
    ).first()
    if existing:
        if desired_token and existing.token != desired_token:
            existing.token = desired_token
            existing.save(update_fields=["token"])
        return existing
    return APIToken.objects.create(
        user=user,
        workspace=workspace,
        label=TOKEN_LABEL,
        description="plane-tug e2e token (CI only)",
        token=desired_token or uuid.uuid4().hex,
        user_type=0,
        is_active=True,
        is_service=False,
        allowed_rate_limit="600/minute",
    )


def ensure_webhook(workspace: Workspace, user: User) -> Webhook:
    """Workspace-level webhook firing at the plane-tug container.

    Subscribes to every per-resource family the Plane Webhook model
    exposes: project, issue, issue_comment, cycle, module. Plane emits
    cycle_issue / module_issue events under the cycle / module
    subscriptions automatically; there is no separate flag for them.

    Forces secret_key to a known value so plane-tug's
    PLANE_TUG_WEBHOOK_SECRET env var can be hard-coded in compose.yaml.
    """
    webhook, created = Webhook.objects.get_or_create(
        workspace=workspace,
        url=WEBHOOK_URL,
        defaults={
            "created_by": user,
            "updated_by": user,
            "is_active": True,
            "secret_key": WEBHOOK_SECRET,
            "project": True,
            "issue": True,
            "issue_comment": True,
            "cycle": True,
            "module": True,
        },
    )
    if not created:
        webhook.is_active = True
        webhook.secret_key = WEBHOOK_SECRET
        webhook.project = True
        webhook.issue = True
        webhook.issue_comment = True
        webhook.cycle = True
        webhook.module = True
        webhook.save()
    return webhook


def main() -> None:
    instance = ensure_instance()
    user = ensure_admin_user()
    ensure_instance_admin(instance, user)
    workspace = ensure_workspace(user)
    project = ensure_project(workspace, user)
    ensure_default_states(project, user)
    token = ensure_api_token(user, workspace)
    webhook = ensure_webhook(workspace, user)

    sys.stderr.write(
        f"seed: instance={instance.instance_id} user={user.email} "
        f"workspace_slug={workspace.slug} workspace_id={workspace.id} "
        f"project_id={project.id} webhook_id={webhook.id}\n"
    )
    print(json.dumps({
        "workspace_slug": workspace.slug,
        "workspace_id": str(workspace.id),
        "project_id": str(project.id),
        "project_identifier": project.identifier,
        "admin_email": user.email,
        "api_token": token.token,
        "webhook_secret": WEBHOOK_SECRET,
        "plane_base_url": "http://localhost:8765",
        "plane_tug_base_url": "http://localhost:8081",
    }))


if __name__ == "__main__":
    main()

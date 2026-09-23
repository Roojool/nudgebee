"""PR follow-up verification — the one scenario type with real GitHub side
effects on every run. Opens a real, namespaced PR against the fixture repo,
posts a real comment, and (after code_analyzer_client drives the follow-up
call) verifies + tears down.

No mock exists for PRFollowupAgent — confirmed by reading
llm/code-analysis/agents/pr_followup_agent.go, which shells out directly to
`gh pr view`/`gh pr comment` with no injectable client, and treats
AddressedComments as a fallback GitHub's own live thread-resolution state
always overrides — so this is the only way to test it at all. See
docs/code-analysis-eval-spec.md §4c/§5c/§6c for the full design and the
/challenge findings that shaped it (namespaced branches + orphan sweep,
re-checking the original fail_to_pass/pass_to_pass alongside the new gap's
test, and why this is tagged as testing the issue-comment trigger path
specifically).
"""

from __future__ import annotations

import json
import subprocess
import tempfile
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from code_fix_verifier import authenticated_url, run_cmd, run_tests

BUCKET_RESOLVED = "resolved"
BUCKET_UNRESOLVED = "unresolved"
BUCKET_NO_OP = "no_op"
BUCKET_SETUP_ERROR = "setup_error"

_BRANCH_PREFIX = "event-code-fix/pr-followup-"
_ORPHAN_MAX_AGE_HOURS = 4


def _gh(args: list[str], cwd: Path | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["gh"] + args, cwd=cwd, capture_output=True, text=True, timeout=60
    )


def _repo_slug(repo_url: str) -> str:
    return repo_url.removeprefix("https://github.com/").removesuffix(".git")


def sweep_orphaned_prs(repo_url: str) -> None:
    """Close any event-code-fix PR left open by a crashed prior run. This is
    the cleanup mechanism for a crash — one run sweeps what a previous one
    left behind — not a separate cron (docs/code-analysis-eval-spec.md §5c).
    Best-effort: a failed sweep must never block the run that calls it.
    """
    try:
        repo = _repo_slug(repo_url)
        listed = _gh(
            [
                "pr",
                "list",
                "--repo",
                repo,
                "--search",
                f"head:{_BRANCH_PREFIX}",
                "--state",
                "open",
                "--json",
                "number,createdAt,url",
            ]
        )
        if listed.returncode != 0:
            return
        prs = json.loads(listed.stdout or "[]")
        cutoff = datetime.now(timezone.utc) - timedelta(hours=_ORPHAN_MAX_AGE_HOURS)
        for pr in prs:
            created = datetime.fromisoformat(pr["createdAt"].replace("Z", "+00:00"))
            if created < cutoff:
                _gh(["pr", "close", pr["url"], "--repo", repo])
    except Exception:
        # This sweep is only a cleanup convenience: gh/network/parse failures
        # must never prevent a benchmark scenario from starting.
        return


def setup_pr(
    pr_followup: dict[str, Any], diff_file: Path, run_id: str
) -> dict[str, Any]:
    """Clone at base_commit, apply the incomplete fix, push a namespaced
    branch, open a real PR, post the review comment.

    Returns {pr_url, pr_branch, repo, before_head_sha} on success, or
    {"error": ...} — an error here is a harness setup failure
    (BUCKET_SETUP_ERROR), never scored against the model.
    """
    fixture_repo = pr_followup["fixture_repo"]
    repo = _repo_slug(fixture_repo["url"])
    branch = f"{_BRANCH_PREFIX}{run_id}-{int(time.time())}"

    with tempfile.TemporaryDirectory(prefix="event_code_fix_pr_") as tmp_dir:
        tmp = Path(tmp_dir)
        clone_dir = tmp / "repo"
        clone = run_cmd(
            [
                "git",
                "clone",
                "--quiet",
                authenticated_url(fixture_repo["url"]),
                str(clone_dir),
            ],
            tmp,
        )
        if clone.returncode != 0:
            return {"error": f"clone failed: {clone.stderr[:500]}"}

        checkout = run_cmd(
            ["git", "checkout", "--quiet", "-b", branch, fixture_repo["base_commit"]],
            clone_dir,
        )
        if checkout.returncode != 0:
            return {"error": f"checkout failed: {checkout.stderr[:500]}"}

        apply_result = run_cmd(
            ["git", "apply", "--whitespace=nowarn", str(diff_file)], clone_dir
        )
        if apply_result.returncode != 0:
            return {
                "error": f"incomplete-fix patch did not apply: {apply_result.stderr[:500]}"
            }

        run_cmd(["git", "add", "-A"], clone_dir)
        commit = run_cmd(
            [
                "git",
                "commit",
                "--quiet",
                "-m",
                "incomplete fix (event_code_fix benchmark harness)",
            ],
            clone_dir,
        )
        if commit.returncode != 0:
            return {"error": f"commit failed: {commit.stderr[:500]}"}

        push = run_cmd(["git", "push", "--quiet", "-u", "origin", branch], clone_dir)
        if push.returncode != 0:
            return {"error": f"push failed: {push.stderr[:500]}"}

        head = run_cmd(["git", "rev-parse", "HEAD"], clone_dir)
        before_head_sha = head.stdout.strip()

        created = _gh(
            [
                "pr",
                "create",
                "--repo",
                repo,
                "--head",
                branch,
                "--title",
                f"[event_code_fix benchmark] {pr_followup.get('name', 'incomplete fix')}",
                "--body",
                "Opened by the event_code_fix benchmark harness — safe to ignore or close.",
            ],
            clone_dir,
        )
        if created.returncode != 0:
            return {"error": f"gh pr create failed: {created.stderr[:500]}"}
        created_lines = created.stdout.strip().splitlines()
        if not created_lines:
            return {"error": "gh pr create succeeded but printed no output"}
        pr_url = created_lines[-1]

        commented = _gh(
            [
                "pr",
                "comment",
                pr_url,
                "--repo",
                repo,
                "--body",
                pr_followup["review_comment"],
            ]
        )
        if commented.returncode != 0:
            return {"error": f"gh pr comment failed: {commented.stderr[:500]}"}

        return {
            "pr_url": pr_url,
            "pr_branch": branch,
            "repo": repo,
            "before_head_sha": before_head_sha,
        }


def verify_followup(
    pr_followup: dict[str, Any],
    pr_url: str,
    pr_branch: str,
    repo: str,
    before_head_sha: str,
) -> dict[str, Any]:
    """Re-fetch the real PR state and score convergence. Returns
    {"bucket": ..., "detail": ...}.

    No new commit at all is bucketed as no_op rather than unresolved — those
    are different failure modes (see docs/code-analysis-eval-spec.md §6c) and
    averaging them would hide which one actually happened.
    """
    view = _gh(["pr", "view", pr_url, "--repo", repo, "--json", "headRefOid"])
    if view.returncode != 0:
        return {
            "bucket": BUCKET_UNRESOLVED,
            "detail": f"could not re-fetch PR state: {view.stderr[:300]}",
        }
    try:
        parsed = json.loads(view.stdout or "{}")
        if not isinstance(parsed, dict):
            raise TypeError("parsed JSON is not an object")
        after_head_sha = parsed.get("headRefOid")
        if not after_head_sha:
            raise KeyError("headRefOid")
    except (json.JSONDecodeError, KeyError, TypeError) as exc:
        return {
            "bucket": BUCKET_UNRESOLVED,
            "detail": f"failed to parse PR head SHA from gh output: {exc}",
        }

    if after_head_sha == before_head_sha:
        return {
            "bucket": BUCKET_NO_OP,
            "detail": "no new commit landed on the PR branch",
        }

    fixture_repo = pr_followup["fixture_repo"]
    test_command = fixture_repo.get("test_command", "pytest -q")

    with tempfile.TemporaryDirectory(prefix="event_code_fix_pr_verify_") as tmp:
        clone_dir = Path(tmp) / "repo"
        clone = run_cmd(
            [
                "git",
                "clone",
                "--quiet",
                "--branch",
                pr_branch,
                authenticated_url(fixture_repo["url"]),
                str(clone_dir),
            ],
            Path(tmp),
        )
        if clone.returncode != 0:
            return {
                "bucket": BUCKET_UNRESOLVED,
                "detail": f"could not clone PR branch: {clone.stderr[:300]}",
            }
        test_dir = (
            clone_dir / fixture_repo["subdir"]
            if fixture_repo.get("subdir")
            else clone_dir
        )

        new_gap_closed = run_tests(
            test_dir, test_command, pr_followup.get("fail_to_pass_after_followup", [])
        )
        original_still_fixed = run_tests(
            test_dir, test_command, fixture_repo.get("fail_to_pass", [])
        )
        original_not_regressed = run_tests(
            test_dir, test_command, fixture_repo.get("pass_to_pass", [])
        )

    if not new_gap_closed:
        return {
            "bucket": BUCKET_UNRESOLVED,
            "detail": "fail_to_pass_after_followup still fails",
        }
    if not original_still_fixed or not original_not_regressed:
        return {
            "bucket": BUCKET_UNRESOLVED,
            "detail": "followup regressed the original fix",
        }
    return {"bucket": BUCKET_RESOLVED, "detail": ""}


def cleanup_pr(pr_url: str, repo: str) -> None:
    """Close the PR after scoring, regardless of outcome."""
    try:
        _gh(["pr", "close", pr_url, "--repo", repo])
    except Exception:
        # Cleanup must not mask the result or exception from the scenario.
        return

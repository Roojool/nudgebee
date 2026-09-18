"""Deterministic code-fix verification — the one component nothing in
llm/agents/common/ does today.

Clone the fixture repo at bug_commit, confirm the bug is still really there
(fixture_drift check — see docs/code-analysis-eval-spec.md §6/§7: this repo is
live and writable, unlike SWE-bench's static published tasks, so this is
re-checked every run, not just at scenario-authoring time), apply the RCA
response's GitDiff, then run FAIL_TO_PASS and PASS_TO_PASS. Deliberately not
diff-exact-match — see swebench/README.md's own warning about that metric.
"""

from __future__ import annotations

import os
import shlex
import subprocess
import tempfile
from pathlib import Path
from typing import Any

BUCKET_FIXTURE_DRIFT = "fixture_drift"
BUCKET_ERROR = "error"
BUCKET_EMPTY_PATCH = "empty_patch"
BUCKET_UNRESOLVED = "unresolved"
BUCKET_RESOLVED = "resolved"


def run_cmd(cmd: list[str], cwd: Path) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=300)


def run_tests(repo_dir: Path, test_command: str, tests: list[str]) -> bool:
    """True if every listed test passes. An empty list is vacuously true."""
    if not tests:
        return True
    result = run_cmd(shlex.split(test_command) + tests, repo_dir)
    return result.returncode == 0


def authenticated_url(url: str) -> str:
    """Inject GITHUB_TOKEN into an https://github.com URL, if set.

    Needed for environments with no ambient git credentials (e.g. the k8s
    Job in k8s/cronjob.yaml) to clone the private fixture repo. A no-op
    locally, where the operator's own git credential helper/SSH agent
    already handles auth.
    """
    token = os.environ.get("GITHUB_TOKEN")
    if token and url.startswith("https://github.com/"):
        return url.replace(
            "https://github.com/", f"https://x-access-token:{token}@github.com/"
        )
    return url


def _clone(fixture_repo: dict[str, Any], dest: Path) -> subprocess.CompletedProcess:
    clone = run_cmd(
        ["git", "clone", "--quiet", authenticated_url(fixture_repo["url"]), str(dest)],
        dest.parent,
    )
    if clone.returncode != 0:
        return clone
    return run_cmd(["git", "checkout", "--quiet", fixture_repo["bug_commit"]], dest)


def verify_fix(fixture_repo: dict[str, Any], git_diff: str) -> dict[str, Any]:
    """Returns {"bucket": ..., "detail": ...}."""
    if not git_diff or not git_diff.strip():
        return {"bucket": BUCKET_EMPTY_PATCH, "detail": "fix stage produced no diff"}

    test_command = fixture_repo.get("test_command", "pytest -q")
    fail_to_pass = fixture_repo.get("fail_to_pass", [])
    pass_to_pass = fixture_repo.get("pass_to_pass", [])

    with tempfile.TemporaryDirectory(prefix="event_code_fix_") as tmp:
        clone_dir = Path(tmp) / "repo"
        clone = _clone(fixture_repo, clone_dir)
        if clone.returncode != 0:
            return {
                "bucket": BUCKET_ERROR,
                "detail": f"clone/checkout failed: {clone.stderr[:500]}",
            }
        # The fixture repo hosts one scenario per top-level subdirectory (one
        # shared repo, per docs/code-analysis-eval-spec.md's "small dedicated
        # repo, one bug per commit" convention) rather than one repo per
        # scenario. `git apply` always runs at the repo root (clone_dir) —
        # that's where .git lives and diff paths are relative to it — but
        # tests run scoped to the scenario's subdirectory so a repo-wide
        # `pytest -q` (or `go test ./...`) doesn't also pick up other
        # scenarios' fixtures.
        test_dir = (
            clone_dir / fixture_repo["subdir"]
            if fixture_repo.get("subdir")
            else clone_dir
        )

        # fixture_drift check: the bug must still be reproducible at bug_commit,
        # every run — not trusted forever from authoring time, since this repo
        # is live and writable.
        if run_tests(test_dir, test_command, fail_to_pass):
            return {
                "bucket": BUCKET_FIXTURE_DRIFT,
                "detail": "fail_to_pass already passes at bug_commit — fixture no longer reproduces the bug",
            }

        patch_file = Path(tmp) / "fix.patch"
        patch_file.write_text(git_diff, encoding="utf-8")
        apply_result = run_cmd(
            ["git", "apply", "--whitespace=nowarn", str(patch_file)], clone_dir
        )
        if apply_result.returncode != 0:
            return {
                "bucket": BUCKET_ERROR,
                "detail": f"patch did not apply: {apply_result.stderr[:500]}",
            }

        if not run_tests(test_dir, test_command, fail_to_pass):
            return {
                "bucket": BUCKET_UNRESOLVED,
                "detail": "fail_to_pass still fails after the patch",
            }

        if not run_tests(test_dir, test_command, pass_to_pass):
            return {
                "bucket": BUCKET_UNRESOLVED,
                "detail": "pass_to_pass regressed after the patch",
            }

        return {"bucket": BUCKET_RESOLVED, "detail": ""}

# event_code_fix runner.
#
# Build context is llm/benchmark (not event_code_fix/), same reasoning as
# swebench/Dockerfile: llm/agents/common (RAGAS/verdict-judge scoring) and
# benchmark_server/common (get_llm/get_embeddings) are reused verbatim rather
# than redeclared here.
#
# Simpler than swebench/Dockerfile in one way, not in another: no swebench
# harness, no kubectl, no per-instance Job submission — this suite's fixture
# repos are plain pytest/pip projects, cloned and tested inline in the same
# process. But it DOES need the gh CLI: pr_followup_verifier.py (scenario
# 008) opens/comments/closes real PRs against the external fixture repo.
#
# Build with:  docker buildx build -f event_code_fix/Dockerfile llm/benchmark
FROM python:3.13-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends git curl \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends gh \
    && apt-get purge -y curl && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
ENV PYTHONPATH=/app

# uv.lock is the native `uv lock` TOML format, not a pip-compatible
# requirements file (despite the name) — `pip install -r uv.lock` fails.
# `uv sync` resolves it into a project .venv; put that venv's bin first on
# PATH so the ENTRYPOINT's plain `python` picks it up without activation.
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/
COPY pyproject.toml uv.lock /app/
RUN uv sync --frozen --no-install-project
ENV PATH="/app/.venv/bin:$PATH"

COPY llm/agents/common /app/llm/agents/common
RUN touch /app/llm/__init__.py /app/llm/agents/__init__.py
COPY benchmark_server/common /app/benchmark_server/common
RUN touch /app/benchmark_server/__init__.py

COPY event_code_fix/*.py /app/event_code_fix/
COPY event_code_fix/scenarios /app/event_code_fix/scenarios

WORKDIR /app/event_code_fix
ENTRYPOINT ["python", "run_event_code_fix.py"]

"""Which LLM credential an account resolves against.

Mirrors llm-server's selectAccountLLMIntegration, so the two services never
disagree about which provider an account is using.

Rows are (integration id, per-account link flag, tenant default). The two flags
come from different places — the link row and the "Default LLM provider"
checkbox — and an account that only sets the second one still has a default.
"""

from rag.core.utils import db_query


class FakeConnection:
    def __init__(self, rows):
        self.rows = rows

    def execute(self, *args, **kwargs):
        return self

    def fetchall(self):
        return self.rows

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def use_rows(monkeypatch, rows):
    monkeypatch.setattr(db_query, "engine", type("E", (), {"connect": lambda self: FakeConnection(rows)})())


def test_flagged_default_wins(monkeypatch):
    use_rows(monkeypatch, [("a", False, False), ("b", True, False)])

    assert db_query.get_default_llm_integration("acct") == "b"


def test_single_integration_needs_no_flag(monkeypatch):
    # Linking exactly one provider is itself the operator's choice for it.
    use_rows(monkeypatch, [("only", None, False)])

    assert db_query.get_default_llm_integration("acct") == "only"


def test_several_linked_none_flagged_falls_through(monkeypatch):
    # Guessing would silently bind the account to a credential nobody chose.
    use_rows(monkeypatch, [("a", False, False), ("b", False, False)])

    assert db_query.get_default_llm_integration("acct") is None


def test_several_flagged_falls_through(monkeypatch):
    use_rows(monkeypatch, [("a", True, False), ("b", True, False)])

    assert db_query.get_default_llm_integration("acct") is None


def test_no_integrations(monkeypatch):
    use_rows(monkeypatch, [])

    assert db_query.get_default_llm_integration("acct") is None


def test_query_failure_is_not_fatal(monkeypatch):
    class Broken:
        def connect(self):
            raise RuntimeError("db down")

    monkeypatch.setattr(db_query, "engine", Broken())

    assert db_query.get_default_llm_integration("acct") is None


def test_tenant_default_is_used_when_no_link_row_is_flagged(monkeypatch):
    # The checkbox marks a provider "Default" for every account it is linked to,
    # without writing the per-account flag. Reading only the flag drops the whole
    # ingest onto the environment defaults instead.
    use_rows(monkeypatch, [("a", False, False), ("b", False, True), ("c", False, False)])

    assert db_query.get_default_llm_integration("acct") == "b"


def test_link_flag_outranks_the_tenant_default(monkeypatch):
    # Per-account is the more specific choice, and llm-server resolves it first.
    use_rows(monkeypatch, [("a", True, False), ("b", False, True)])

    assert db_query.get_default_llm_integration("acct") == "a"


def test_several_tenant_defaults_falls_through(monkeypatch):
    use_rows(monkeypatch, [("a", False, True), ("b", False, True)])

    assert db_query.get_default_llm_integration("acct") is None

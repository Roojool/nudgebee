from src.orders.repository import OrdersRepository


class FakeDb:
    """Counts queries instead of measuring real latency — see
    docs/code-analysis-eval-spec.md, scenario 003, for why a call-count
    assertion is the deterministic stand-in for "badly optimized query"."""

    def __init__(self, orders, customers):
        self._orders = orders
        self._customers = {c["id"]: c for c in customers}
        self.call_count = 0

    def query(self, sql, *params):
        self.call_count += 1
        if "FROM orders" in sql:
            return [dict(o) for o in self._orders if o["customer_email"] == params[0]]
        if "FROM customers" in sql:
            customer = self._customers.get(params[0])
            return [customer] if customer else []
        raise ValueError(f"unexpected query: {sql}")


def _sample_db():
    orders = [
        {"id": 1, "customer_id": "c1", "customer_email": "a@example.com"},
        {"id": 2, "customer_id": "c1", "customer_email": "a@example.com"},
    ]
    customers = [{"id": "c1", "name": "Ada"}]
    return FakeDb(orders, customers)


def test_returns_correct_orders():
    repo = OrdersRepository(_sample_db())
    orders = repo.get_orders_by_email("a@example.com")
    assert [o["id"] for o in orders] == [1, 2]
    assert orders[0]["customer"]["name"] == "Ada"


def test_single_query_per_request():
    """FAIL_TO_PASS: one request must issue at most 2 queries total (the
    orders lookup plus one customer join), not one customer lookup per order.

    Fails at bug_commit — 2 orders means 1 orders query + 2 per-order
    customer lookups = 3 calls. Passes once the repository joins instead of
    looping.
    """
    db = _sample_db()
    OrdersRepository(db).get_orders_by_email("a@example.com")
    assert db.call_count <= 2

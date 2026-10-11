"""Toy orders repository for the event_code_fix `n_plus_one_query_latency`
scenario.

This is the bug_commit version: get_orders_by_email() fetches the matching
orders, then issues one extra customer-lookup query per order (N+1) instead
of a single joined query — the root cause the scenario's repeated "slow
query" log lines on /api/orders/by-email describe.
"""


class OrdersRepository:
    def __init__(self, db):
        self._db = db  # anything exposing .query(sql, *params) -> list[dict]

    def get_orders_by_email(self, email):
        orders = self._db.query("SELECT * FROM orders WHERE customer_email = %s", email)
        for order in orders:
            customer_rows = self._db.query(
                "SELECT * FROM customers WHERE id = %s", order["customer_id"]
            )
            order["customer"] = customer_rows[0] if customer_rows else None
        return orders

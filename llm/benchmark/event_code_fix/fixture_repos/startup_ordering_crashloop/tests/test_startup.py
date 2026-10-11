import threading
import time

from src.startup import App


def test_health_reports_true_after_run_with_no_delay():
    app = App(slow_setup_seconds=0)
    app.run()
    assert app.is_healthy()


def test_health_server_starts_before_slow_setup_completes():
    """FAIL_TO_PASS: the health endpoint must become ready within a tight,
    readiness-probe-like deadline regardless of how long setup takes.

    Fails at bug_commit — run() does setup synchronously before starting the
    health server, so a 5s setup blocks readiness well past a 1s deadline.
    Passes once setup is backgrounded and the health server starts first.
    """
    app = App(slow_setup_seconds=5)
    thread = threading.Thread(target=app.run, daemon=True)
    thread.start()

    deadline = time.monotonic() + 1.0
    while time.monotonic() < deadline:
        if app.is_healthy():
            break
        time.sleep(0.01)

    assert (
        app.is_healthy()
    ), "health server did not become ready within the readiness-probe deadline"

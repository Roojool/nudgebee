"""Root test conftest.

Importing any `server.*` module pulls in `server.utils.utils`, whose `DBConfig`
raises at import time if `ML_INFERENCE_DATABASE_URL` is unset. Tests never touch the
inference DB, so we set a harmless default here — loaded before any sibling conftest
or test module imports `server`. `setdefault` means a real value in the environment
(CI / local dev) always wins.
"""

import os

for _key, _val in {
    "ML_INFERENCE_DATABASE_URL": "sqlite://",
    "RELAY_SERVER_ENDPOINT": "http://localhost:8080",
    "RELAY_SERVER_SECRET_KEY": "test-secret",
    "ML_MODEL_STORE_BUCKET": "test-bucket",
    # The api-server metrics transport reads these at import; a test that wants the
    # relay path pins ML_METRICS_TRANSPORT itself.
    "SERVICE_API_SERVER_URL": "http://services-server:8000",
    "ACTION_API_SERVER_TOKEN": "test-action-token",
}.items():
    os.environ.setdefault(_key, _val)

from unittest.mock import MagicMock

from notifications_server.services.common import CommonService


def test_thread_reply_does_not_unfurl_links_by_default():
    service = CommonService(engine=MagicMock(), slack_app=MagicMock(), teams_app=MagicMock())
    service.get_slack_installation = MagicMock(return_value=MagicMock(token="tok"))

    service.slack_reply_in_thread("C1", "T1", "111.222", "Working on it. <https://app.example.com/x|View in App>")

    kwargs = service.slack_app.client.reply_in_thread.call_args.kwargs
    assert kwargs["unfurl_links"] is False
    assert kwargs["unfurl_media"] is False

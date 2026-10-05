import http.client
import json
import threading
import unittest

from tool_gateway import server


class MockGatewayProtocolTest(unittest.TestCase):
    def setUp(self):
        handler = server.make_handler(
            {"web_search": "mock"},
            bearer_token="",
            log_file_path=None,
        )
        self.httpd = server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever)
        self.thread.start()

    def tearDown(self):
        self.httpd.shutdown()
        self.thread.join()
        self.httpd.server_close()

    def test_mock_gateway_returns_production_tool_envelope(self):
        conn = http.client.HTTPConnection("127.0.0.1", self.httpd.server_port, timeout=3)
        payload = {
            "tool_call_id": "call_mock",
            "tool_name": "web_search",
            "arguments": '{"query":"cross platform"}',
        }
        conn.request("POST", "/api/agent/tool_call", json.dumps(payload), {"Content-Type": "application/json"})
        response = conn.getresponse()
        body = json.loads(response.read().decode("utf-8"))
        conn.close()

        self.assertEqual(response.status, 200)
        self.assertEqual(body["tool_call_id"], "call_mock")
        self.assertEqual(body["error"], "")
        result = json.loads(body["result"])
        self.assertEqual(result, {"ok": True, "tool": "web_search", "mock": True, "arguments": {"query": "cross platform"}})


if __name__ == "__main__":
    unittest.main()

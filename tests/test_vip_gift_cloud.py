"""只向临时 SQLite 与回环地址发送合成数据，不调用真实赠送或领取服务。"""

import importlib.util
import gc
import json
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "tools" / "vip_member_gift_cloud" / "vip_member_gift_cloud.py"
spec = importlib.util.spec_from_file_location("vip_gift_cloud_test_target", SCRIPT)
cloud = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cloud)


class CloudAPITests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        store = cloud.VipGiftStore(str(Path(self.directory.name) / "synthetic.sqlite3"), 7, priority_donors=["111111"])
        self.server = cloud.VipGiftHTTPServer(("127.0.0.1", 0), cloud.ApiHandler, store, "synthetic-test-key")
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.enterContext(mock.patch.object(cloud.ApiHandler, "log_message"))
        self.thread.start()
        self.addCleanup(self.stop_server)
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        # 绕过主机 HTTP 代理，测试流量只允许进入本次临时回环服务。
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.month = cloud.current_month()

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        # sqlite3 的事务上下文不负责关闭连接，回收请求完成后的引用再清理临时库。
        gc.collect()

    def request(self, path, body=None, authenticated=True, api_key=False):
        headers = {"Content-Type": "application/json"}
        if authenticated:
            headers["X-Api-Key" if api_key else "Authorization"] = "synthetic-test-key" if api_key else "Bearer synthetic-test-key"
        data = json.dumps(body).encode("utf-8") if body is not None else None
        request = urllib.request.Request(self.base + path, data=data, headers=headers)
        try:
            response = self.http.open(request, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, json.loads(response.read())

    def publish(self, token="TEST_ONLY_TOKEN", donor="111111", days=21):
        status, response = self.request("/tokens/upsert", {"token": token, "donorUid": donor, "month": self.month, "availableDays": days, "totalDays": days, "source": "unit-test"})
        self.assertEqual(status, 200)
        self.assertTrue(response["ok"])
        return response["data"]

    def test_health_and_authentication_boundary(self):
        status, health = self.request("/health", authenticated=False)
        self.assertEqual(status, 200)
        self.assertEqual(health["priorityDonors"], ["111111"])
        status, response = self.request("/stats", authenticated=False)
        self.assertEqual((status, response["error"]), (401, "unauthorized"))
        self.assertEqual(self.request("/stats", api_key=True)[0], 200)
        self.assertEqual(self.request("/unknown")[0], 404)

    def test_claim_is_idempotent_and_deducts_actual_days(self):
        self.publish()
        status, available = self.request(f"/tokens/available?month={self.month}&receiverUid=222222")
        self.assertEqual(status, 200)
        self.assertEqual(available["data"]["token"]["token"], "TEST_ONLY_TOKEN")
        body = {"token": "TEST_ONLY_TOKEN", "month": self.month, "receiverUid": "222222", "duration": 7, "recordId": "test-claim"}
        self.assertEqual(self.request("/claims/success", body)[0], 200)
        self.assertEqual(self.request("/claims/success", body)[0], 200)
        status, response = self.request(f"/claims/status?month={self.month}&receiverUid=222222")
        self.assertEqual(status, 200)
        self.assertTrue(response["data"]["claimed"])
        _, available = self.request(f"/tokens/available?month={self.month}&receiverUid=333333")
        self.assertEqual(available["data"]["token"]["available_days"], 14)

    def test_terminal_failure_stops_distribution_and_invalid_input_is_rejected(self):
        self.publish()
        self.assertEqual(self.request("/tokens/upsert", {})[0], 400)
        status, response = self.request("/tokens/fail", {"token": "TEST_ONLY_TOKEN", "month": self.month, "receiverUid": "222222", "reason": "failed", "message": "synthetic 12200"})
        self.assertEqual(status, 200)
        self.assertEqual(response["data"]["token"]["status"], "invalid")
        _, available = self.request(f"/tokens/available?month={self.month}&receiverUid=333333")
        self.assertIsNone(available["data"]["token"])


if __name__ == "__main__":
    unittest.main()

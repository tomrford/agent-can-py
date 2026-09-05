"""Exercise the installed launcher and executable over real stdio MCP."""

import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import threading

import pytest

from agent_can import __version__


class Client:
    def __init__(self):
        self.process = subprocess.Popen(
            [sys.executable, "-m", "agent_can"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self.messages = queue.Queue()
        self.sequence = 0
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()
        self.rpc("initialize", {
            "protocolVersion": "2025-11-25", "capabilities": {},
            "clientInfo": {"name": "package-test", "version": "1"},
        })
        self.write({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def read(self):
        for line in self.process.stdout:
            self.messages.put(json.loads(line))
        self.messages.put(None)

    def write(self, message):
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def rpc(self, method, params):
        self.sequence += 1
        self.write({"jsonrpc": "2.0", "id": self.sequence, "method": method, "params": params})
        while True:
            response = self.messages.get(timeout=10)
            assert response is not None, self.process.stderr.read()
            if response.get("id") == self.sequence:
                assert "error" not in response, response
                return response["result"]

    def tool(self, name, arguments, error=False):
        result = self.rpc("tools/call", {"name": name, "arguments": arguments})
        assert result.get("isError", False) is error, result
        if error:
            return result
        return result["structuredContent"]

    def close(self):
        self.process.stdin.close()
        try:
            code = self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
            raise
        assert code == 0, self.process.stderr.read()


@pytest.fixture
def client():
    connection = Client()
    yield connection
    connection.close()


def test_installed_version():
    result = subprocess.run(
        [sys.executable, "-m", "agent_can", "--version"],
        capture_output=True, text=True, check=True, timeout=10,
    )
    assert result.stdout.strip() == f"agent-can {__version__}"
    assert result.stderr == ""


def test_stdio_capabilities_and_session_lifecycle(client):
    tools = client.rpc("tools/list", {})["tools"]
    assert {tool["name"] for tool in tools} == {
        "buses_list", "connect", "disconnect", "status", "schema", "message_list",
        "message_read", "frame_send", "message_send", "message_stop", "trace_start", "trace_stop",
    }
    assert all(tool["inputSchema"]["type"] == "object" for tool in tools)
    buses = client.tool("buses_list", {})["buses"]
    virtual = next(bus for bus in buses if bus["interface"] == "virtual")
    request = {"channel": virtual["channel"]}
    assert client.tool("connect", request)["created"]
    assert client.tool("connect", request)["already_connected"]
    client.tool("frame_send", {"target": "0x123", "data": "AABB", "periodicity_ms": 5})
    assert client.tool("status", {})["periodic_schedules"][0]["state"] == "active"
    observation = client.tool("message_read", {"select": "0x123", "direction": "tx"})["observations"][0]
    assert observation["payload_hex"] == "AABB"
    assert observation["len"] == 2
    assert client.tool("message_stop", {"target": "0x0123"})["stopped"]
    assert client.tool("disconnect", {})["disconnected"]
    assert client.tool("status", {})["connection_state"] == "disconnected"
    assert client.tool("connect", request)["created"]


def test_eof_finalises_recording_and_periodic_sends(tmp_path):
    client = Client()
    trace = tmp_path / "final.asc"
    try:
        client.tool("connect", {"channel": "virtual:agent-can"})
        client.tool("trace_start", {"path": str(trace)})
        client.tool("frame_send", {"target": "0x321", "data": "DEADBEEF", "periodicity_ms": 2})
    finally:
        client.close()
    text = trace.read_text()
    assert "321 Tx d 4 DE AD BE EF" in text
    assert text.endswith("End TriggerBlock\n")


def test_lossless_integer_and_nonfinite_signals(client, tmp_path):
    dbc = tmp_path / "numbers.dbc"
    dbc.write_text('''VERSION ""
NS_ :
BS_:
BU_: ECU
BO_ 512 Integer: 8 ECU
 SG_ value : 0|64@1+ (1,0) [0|18446744073709551615] "" ECU
BO_ 513 Floating: 4 ECU
 SG_ value : 0|32@1+ (1,0) [0|100] "V" ECU
SIG_VALTYPE_ 513 value : 1;
''')
    client.tool("connect", {
        "channel": "virtual:agent-can",
        "dbcs": [{"alias": "test", "path": str(dbc)}],
    })
    for value in (9007199254740993, 18446744073709551615):
        sent = client.tool("message_send", {"target": "test.Integer", "signals": {"value": value}})
        assert sent["payload_hex"] == value.to_bytes(8, "little").hex().upper()
        read = client.tool("message_read", {"select": "test.Integer", "direction": "tx"})
        assert read["observations"][0]["signals"]["value"]["value"] == value
    # Keep the literal JSON number: converting it to a Python float would
    # round it before it even reached the server.
    client.sequence += 1
    client.process.stdin.write(
        '{"jsonrpc":"2.0","id":' + str(client.sequence)
        + ',"method":"tools/call","params":{"name":"message_send",'
        '"arguments":{"target":"test.Integer","signals":{"value":9007199254740993.0}}}}\n'
    )
    client.process.stdin.flush()
    response = client.messages.get(timeout=10)
    assert response["id"] == client.sequence
    assert response["result"]["isError"]
    client.tool("frame_send", {"target": "0x201", "data": "0000C07F"})
    read = client.tool("message_read", {"select": "test.Floating", "direction": "tx"})
    observation = read["observations"][0]
    assert observation["payload_hex"] == "0000C07F"
    assert "value" in observation["signal_errors"]


def test_terminating_launcher_stops_child_with_stdin_open(tmp_path):
    client = Client()
    trace = tmp_path / "terminated.asc"
    try:
        client.tool("connect", {"channel": "virtual:agent-can"})
        client.tool("trace_start", {"path": str(trace)})
        client.tool("frame_send", {"target": "0x321", "data": "AA", "periodicity_ms": 2})
        client.process.terminate()
        client.process.wait(timeout=10)
        # stdout is inherited by Go on Windows; EOF proves that child stopped
        # while this test still holds the stdin writer open.
        client.reader.join(timeout=10)
        assert not client.reader.is_alive(), "native process survived launcher termination"
        assert trace.read_text().endswith("End TriggerBlock\n")
    finally:
        client.process.stdin.close()
        if client.process.poll() is None:
            client.process.kill()
            client.process.wait()


def test_trace_format_and_generated_path(client):
    client.tool("connect", {"channel": "virtual:agent-can"})
    client.tool("trace_start", {"format": "blf"}, error=True)
    trace = client.tool("trace_start", {"format": "asc"})
    path = Path(trace["path"])
    try:
        assert path.is_absolute() and path.suffix == ".asc"
        assert trace["format"] == "asc"
        client.tool("frame_send", {"target": "0x100", "data": "01"})
        assert client.tool("trace_stop", {})["state"] == "stopped"
        saved = path.read_bytes()
        client.tool("trace_start", {"path": str(path)}, error=True)
        assert path.read_bytes() == saved
    finally:
        os.unlink(path)

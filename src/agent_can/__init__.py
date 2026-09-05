"""Agent-first CAN control MCP server."""

__all__ = ["__version__"]
__version__ = "0.2.0"


def main() -> None:
    """Launch the bundled executable with the caller's stdio unchanged."""
    import os
    from pathlib import Path
    import subprocess
    import sys

    name = "agent-can.exe" if os.name == "nt" else "agent-can"
    executable = str(Path(__file__).parent / "bin" / name)
    args = [executable, *sys.argv[1:]]
    if os.name != "nt":
        os.execv(executable, args)
    # Windows has no POSIX exec replacement. Go watches this process so a
    # terminated launcher cannot leave periodic CAN transmissions running.
    env = dict(os.environ, AGENT_CAN_LAUNCHER_PID=str(os.getpid()))
    child = subprocess.Popen(args, env=env)
    try:
        code = child.wait()
    except KeyboardInterrupt:
        # Both processes receive the console interrupt; let Go finalise traces.
        code = child.wait()
    raise SystemExit(code)

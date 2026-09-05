"""Build one native Go executable into each platform wheel."""

import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import sysconfig

from hatchling.builders.hooks.plugin.interface import BuildHookInterface


class NativeBuildHook(BuildHookInterface):
    def initialize(self, version: str, build_data: dict) -> None:
        del version
        root = Path(self.root)
        env = dict(os.environ, CGO_ENABLED="0", GOWORK="off")
        target = json.loads(
            subprocess.check_output(
                ["go", "env", "-json", "GOOS", "GOARCH", "GOVERSION"],
                cwd=root,
                env=env,
                text=True,
            )
        )
        system = {"darwin": "darwin", "linux": "linux", "win32": "windows"}.get(sys.platform)
        machine = platform.machine().lower()
        architecture = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(machine)
        if target["GOOS"] != system or target["GOARCH"] != architecture:
            raise RuntimeError("Build wheels on the target platform; cross compilation would mislabel them")
        name = "agent-can.exe" if os.name == "nt" else "agent-can"
        # Editable installs import from src, so keep their binary beside the
        # launcher too. force_include also puts it into a regular wheel.
        executable = root / "src" / "agent_can" / "bin" / name
        executable.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            [
                "go", "build", "-mod=readonly", "-trimpath",
                "-ldflags", f"-s -w -X main.version={self.metadata.version}",
                "-o", str(executable), "./cmd/agent-can",
            ],
            cwd=root, env=env, check=True,
        )
        tag = sysconfig.get_platform().replace("-", "_").replace(".", "_")
        if sys.platform == "darwin":
            # Go 1.25/1.26 support macOS 12. For other compilers, keep the
            # conservative host tag until its deployment minimum is verified.
            if target["GOVERSION"].startswith(("go1.25.", "go1.26.")):
                tag = f"macosx_12_0_{platform.machine()}"
        elif sys.platform == "linux":
            if platform.libc_ver()[0] != "glibc":
                raise RuntimeError("Build Linux wheels on glibc; musl wheels are not currently packaged")
            tag = f"manylinux_2_17_{platform.machine()}"
        build_data["pure_python"] = False
        build_data["tag"] = f"py3-none-{tag}"
        build_data["force_include"][str(executable)] = f"agent_can/bin/{name}"

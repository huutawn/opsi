#!/usr/bin/env python3
import argparse
import hashlib
import hmac
import json
import os
import pathlib
import re
import signal
import stat
import sys
import termios
import time


MAX_HANDOFF_SIZE = 512
MAX_REDACTION_VALUES_SIZE = 4096
MAX_SECRET_RESPONSE_SIZE = 4096
TOTP_PERIOD_SECONDS = 30
IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
CODE = re.compile(r"^[0-9]{6}$")
SENSITIVE_FIELD = (
    r"token|agent_token|registration_token|pat|private_key|kubeconfig|"
    r"app_secret|password|otp_code|otp_request_id|totp_code|totp_seed|totp_secret"
)
PROTECTED_CREDENTIAL_FIELDS = ("username", "password")


def _no_duplicate_fields(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("JSON contains duplicate fields")
        result[key] = value
    return result


def _absolute(path, label):
    path = pathlib.Path(path)
    if not path.is_absolute():
        raise ValueError(f"{label} must be absolute")
    return path


def _reject_symlink_components(path, label):
    current = pathlib.Path(path.anchor)
    for part in path.parts[1:]:
        current /= part
        try:
            info = current.lstat()
        except FileNotFoundError:
            return
        if stat.S_ISLNK(info.st_mode):
            raise ValueError(f"{label} must not contain symlinks")


def _validate_private_directory(path, label):
    path = _absolute(path, label)
    _reject_symlink_components(path, label)
    try:
        info = path.lstat()
    except FileNotFoundError as exc:
        raise ValueError(f"{label} is missing") from exc
    if stat.S_ISLNK(info.st_mode):
        raise ValueError(f"{label} must not be a symlink")
    if not stat.S_ISDIR(info.st_mode):
        raise ValueError(f"{label} must be a directory")
    if info.st_uid != os.geteuid():
        raise ValueError(f"{label} must be owned by the current user")
    if stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError(f"{label} must use mode 0700")
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | getattr(os, "O_NOFOLLOW", 0))
    try:
        current = os.fstat(fd)
        if (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino):
            raise ValueError(f"{label} changed during validation")
    finally:
        os.close(fd)
    return path


def validate_handoff_directory(path):
    return _validate_private_directory(path, "second-factor handoff directory")


def validate_timeout(value):
    if isinstance(value, bool):
        raise ValueError("second-factor timeout must be between 1 and 120 seconds")
    try:
        value = int(value)
    except (TypeError, ValueError) as exc:
        raise ValueError("second-factor timeout must be between 1 and 120 seconds") from exc
    if not 1 <= value <= 120:
        raise ValueError("second-factor timeout must be between 1 and 120 seconds")
    return value


def _validate_private_file(path, label, maximum, allow_empty=False):
    path = _absolute(path, label)
    _reject_symlink_components(path, label)
    try:
        info = path.lstat()
    except FileNotFoundError as exc:
        raise ValueError(f"{label} is missing") from exc
    if stat.S_ISLNK(info.st_mode):
        raise ValueError(f"{label} must not be a symlink")
    if not stat.S_ISREG(info.st_mode):
        raise ValueError(f"{label} must be a regular file")
    if info.st_uid != os.geteuid():
        raise ValueError(f"{label} must be owned by the current user")
    if stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError(f"{label} must use mode 0600")
    minimum = 0 if allow_empty else 1
    if not minimum <= info.st_size <= maximum:
        raise ValueError(f"{label} size is invalid")
    return info


def _read_private_file(path, label, maximum, allow_empty=False):
    expected = _validate_private_file(path, label, maximum, allow_empty)
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        current = os.fstat(fd)
        if (current.st_dev, current.st_ino) != (expected.st_dev, expected.st_ino):
            raise ValueError(f"{label} changed during validation")
        data = os.read(fd, maximum + 1)
    finally:
        os.close(fd)
    minimum = 0 if allow_empty else 1
    if not minimum <= len(data) <= maximum:
        raise ValueError(f"{label} size is invalid")
    return data, current


def _validate_redaction_value(value):
    if (
        not isinstance(value, str)
        or not value
        or len(value) > 256
        or value.splitlines() != [value]
    ):
        raise ValueError("redaction value is malformed")
    try:
        value.encode("utf-8")
    except UnicodeEncodeError as exc:
        raise ValueError("redaction value is malformed") from exc


def _decode_redaction_values(data):
    if data and not data.endswith(b"\n"):
        raise ValueError("redaction values file is malformed")
    try:
        values = data[:-1].decode("utf-8").split("\n") if data else []
    except UnicodeDecodeError as exc:
        raise ValueError("redaction values file is malformed") from exc
    try:
        for value in values:
            _validate_redaction_value(value)
    except ValueError as exc:
        raise ValueError("redaction values file is malformed") from exc
    return tuple(dict.fromkeys(values))


def read_redaction_values(path):
    data, _ = _read_private_file(
        path, "redaction values file", MAX_REDACTION_VALUES_SIZE, allow_empty=True
    )
    return _decode_redaction_values(data)


def _write_all(fd, payload):
    written = 0
    while written < len(payload):
        count = os.write(fd, payload[written:])
        if count <= 0:
            raise OSError("redaction values file write made no progress")
        written += count


def _append_redaction_values(path, values):
    requested = tuple(values)
    for value in requested:
        _validate_redaction_value(value)
    path = _absolute(path, "redaction values file")
    data, expected = _read_private_file(
        path, "redaction values file", MAX_REDACTION_VALUES_SIZE, allow_empty=True
    )
    existing = _decode_redaction_values(data)
    merged = tuple(dict.fromkeys((*existing, *requested)))
    payload = "".join(f"{value}\n" for value in merged).encode("utf-8")
    if len(payload) > MAX_REDACTION_VALUES_SIZE:
        raise ValueError("redaction values file size is invalid")
    if merged == existing:
        return

    directory_fd = os.open(
        path.parent, os.O_RDONLY | os.O_DIRECTORY | getattr(os, "O_NOFOLLOW", 0)
    )
    temporary = f".{path.name}.{os.getpid()}.{time.time_ns()}"
    fd = -1
    created = False
    published = False
    try:
        current = os.stat(path.name, dir_fd=directory_fd, follow_symlinks=False)
        if (current.st_dev, current.st_ino) != (expected.st_dev, expected.st_ino):
            raise ValueError("redaction values file changed during validation")

        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
        fd = os.open(temporary, flags, 0o600, dir_fd=directory_fd)
        created = True
        os.fchmod(fd, 0o600)
        info = os.fstat(fd)
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError("temporary redaction values file is insecure")
        _write_all(fd, payload)
        os.fsync(fd)
        os.close(fd)
        fd = -1

        current = os.stat(path.name, dir_fd=directory_fd, follow_symlinks=False)
        if (current.st_dev, current.st_ino) != (expected.st_dev, expected.st_ino):
            raise ValueError("redaction values file changed during publication")
        os.replace(
            temporary,
            path.name,
            src_dir_fd=directory_fd,
            dst_dir_fd=directory_fd,
        )
        published = True
        os.fsync(directory_fd)
    finally:
        try:
            if fd >= 0:
                os.close(fd)
        finally:
            try:
                if created and not published:
                    try:
                        os.unlink(temporary, dir_fd=directory_fd)
                    except FileNotFoundError:
                        pass
            finally:
                os.close(directory_fd)

    if read_redaction_values(path) != merged:
        raise ValueError("redaction value registration verification failed")


def _write_private_json(path, payload):
    path = _absolute(path, "second-factor request path")
    _validate_private_directory(path.parent, "second-factor request directory")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o600)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            json.dump(payload, output, separators=(",", ":"), sort_keys=True)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
    except Exception:
        try:
            path.unlink()
        except FileNotFoundError:
            pass
        raise


def _decode_handoff(raw, wall_clock):
    try:
        payload = json.loads(raw.decode("utf-8"), object_pairs_hook=_no_duplicate_fields)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ValueError("second-factor handoff is malformed") from exc
    if not isinstance(payload, dict):
        raise ValueError("second-factor handoff is malformed")
    factor_type = payload.get("type")
    if factor_type == "totp":
        if set(payload) != {"type", "code", "period_start_unix"}:
            raise ValueError("TOTP handoff schema is invalid")
        code = payload.get("code")
        period_start = payload.get("period_start_unix")
        if not isinstance(code, str) or not CODE.fullmatch(code):
            raise ValueError("TOTP handoff code is malformed")
        if isinstance(period_start, bool) or not isinstance(period_start, int):
            raise ValueError("TOTP handoff period is malformed")
        if period_start % TOTP_PERIOD_SECONDS:
            raise ValueError("TOTP handoff period is malformed")
        now = int(wall_clock())
        expires_at = period_start + TOTP_PERIOD_SECONDS
        if now >= expires_at:
            raise ValueError("TOTP handoff is expired")
        if now < period_start:
            raise ValueError("TOTP handoff period is in the future")
        return {
            "method": "totp",
            "code": code,
            "expires_at_unix": expires_at,
            "fingerprint": "",
            "redaction_values": (code,),
        }
    if factor_type == "otp":
        if set(payload) != {"type", "request_id", "code"}:
            raise ValueError("OTP handoff schema is invalid")
        request_id = payload.get("request_id")
        code = payload.get("code")
        if not isinstance(request_id, str) or not IDENTIFIER.fullmatch(request_id):
            raise ValueError("OTP handoff request ID is malformed")
        if not isinstance(code, str) or not CODE.fullmatch(code):
            raise ValueError("OTP handoff code is malformed")
        fingerprint = hashlib.sha256(request_id.encode()).hexdigest()
        return {
            "method": "otp",
            "request_id": request_id,
            "code": code,
            "expires_at_unix": 0,
            "fingerprint": fingerprint,
            "redaction_values": (request_id, code),
        }
    raise ValueError("second-factor handoff type must be totp or otp")


def _take_handoff(directory, operation):
    name = f"{operation}.json"
    directory_fd = os.open(
        directory, os.O_RDONLY | os.O_DIRECTORY | getattr(os, "O_NOFOLLOW", 0)
    )
    try:
        try:
            info = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
        except FileNotFoundError:
            return None
        try:
            if stat.S_ISLNK(info.st_mode):
                raise ValueError("second-factor handoff must not be a symlink")
            if not stat.S_ISREG(info.st_mode):
                raise ValueError("second-factor handoff must be a regular file")
            if info.st_uid != os.geteuid():
                raise ValueError("second-factor handoff must be owned by the current user")
            if stat.S_IMODE(info.st_mode) != 0o600:
                raise ValueError("second-factor handoff must use mode 0600")
            if not 1 <= info.st_size <= MAX_HANDOFF_SIZE:
                raise ValueError("second-factor handoff size is invalid")
            fd = os.open(
                name,
                os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0),
                dir_fd=directory_fd,
            )
            try:
                current = os.fstat(fd)
                if (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino):
                    raise ValueError("second-factor handoff changed during validation")
                raw = os.read(fd, MAX_HANDOFF_SIZE + 1)
            finally:
                os.close(fd)
            if not 1 <= len(raw) <= MAX_HANDOFF_SIZE:
                raise ValueError("second-factor handoff size is invalid")
            return raw
        finally:
            os.unlink(name, dir_fd=directory_fd)
    finally:
        os.close(directory_fd)


def consume_handoff(
    directory,
    operation,
    timeout_seconds,
    output_path,
    redaction_values_path,
    service_id,
    secret_name,
    *,
    paired_output_path=None,
    reject_fingerprint="",
    wall_clock=time.time,
    monotonic=time.monotonic,
    sleep=time.sleep,
):
    directory = validate_handoff_directory(directory)
    timeout_seconds = validate_timeout(timeout_seconds)
    if operation not in {"rotate", "reveal"}:
        raise ValueError("second-factor operation is invalid")
    if not IDENTIFIER.fullmatch(service_id) or not IDENTIFIER.fullmatch(secret_name):
        raise ValueError("secret identity is invalid")
    deadline = monotonic() + timeout_seconds
    while True:
        raw = _take_handoff(directory, operation)
        if raw is not None:
            break
        if monotonic() >= deadline:
            raise TimeoutError(f"timed out waiting for {operation} second-factor handoff")
        sleep(min(0.1, max(0, deadline - monotonic())))
    factor = _decode_handoff(raw, wall_clock)
    if reject_fingerprint and factor["method"] == "otp" and hmac.compare_digest(
        reject_fingerprint, factor["fingerprint"]
    ):
        raise ValueError("OTP handoff pair was reused")
    _append_redaction_values(redaction_values_path, factor["redaction_values"])
    request = {
        "service_id": service_id,
        "name": secret_name,
        "namespace": "default",
    }
    if operation == "reveal":
        request["reveal"] = True
    if factor["method"] == "totp":
        request["totp_code"] = factor["code"]
    else:
        request.update(
            otp_request_id=factor["request_id"], otp_code=factor["code"]
        )
    _write_private_json(output_path, request)
    if paired_output_path and operation == "rotate" and factor["method"] == "totp":
        reveal_request = dict(request, reveal=True)
        _write_private_json(paired_output_path, reveal_request)
    return {
        "method": factor["method"],
        "expires_at_unix": factor["expires_at_unix"],
        "fingerprint": factor["fingerprint"],
    }


def redact_text(text, secrets):
    for secret in sorted((value for value in secrets if value), key=len, reverse=True):
        text = text.replace(secret, "[REDACTED]")
    patterns = (
        (re.compile(r"(?i)(authorization\s*[:=]\s*bearer\s+)[^\s\",}]+"), r"\1[REDACTED]"),
        (
            re.compile(rf'(?i)((?:"?(?:{SENSITIVE_FIELD})"?)\s*[:=]\s*")([^"]*)(")'),
            r"\1[REDACTED]\3",
        ),
        (
            re.compile(rf"(?i)((?:{SENSITIVE_FIELD})\s*[:=]\s*)([^\s,}}]+)"),
            r"\1[REDACTED]",
        ),
        (
            re.compile(rf"(?i)([?&](?:{SENSITIVE_FIELD})=)[^&#\s]+"),
            r"\1[REDACTED]",
        ),
    )
    for pattern, replacement in patterns:
        text = pattern.sub(replacement, text)
    return text


def redact_secret_response(raw, redaction_values_path):
    if not isinstance(raw, bytes) or not 1 <= len(raw) <= MAX_SECRET_RESPONSE_SIZE:
        raise ValueError("secret response size is invalid")
    try:
        payload = json.loads(raw.decode("utf-8"), object_pairs_hook=_no_duplicate_fields)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ValueError("secret response is malformed") from exc
    if not isinstance(payload, dict):
        raise ValueError("secret response is malformed")

    credentials = []
    for field in PROTECTED_CREDENTIAL_FIELDS:
        if field not in payload:
            continue
        value = payload[field]
        if not isinstance(value, str) or len(value) > 256:
            raise ValueError("secret response credential is malformed")
        if value:
            credentials.append(value)
        payload[field] = "[REDACTED]"

    _append_redaction_values(redaction_values_path, credentials)
    redaction_values = read_redaction_values(redaction_values_path)
    encoded = json.dumps(payload, separators=(",", ":"), sort_keys=True)
    return redact_text(encoded, redaction_values) + "\n"


def _write_tty(fd, value):
    while value:
        written = os.write(fd, value)
        if written <= 0:
            raise OSError("controlling TTY write failed")
        value = value[written:]


def read_tty_secret(prompt, maximum):
    fd = -1
    original = None
    prompted = False
    handlers = {}

    def interrupt(_signum, _frame):
        raise InterruptedError("secret input interrupted")

    try:
        flags = os.O_RDWR | getattr(os, "O_NOFOLLOW", 0)
        fd = os.open("/dev/tty", flags)
        info = os.fstat(fd)
        if (
            not stat.S_ISCHR(info.st_mode)
            or not os.isatty(fd)
            or os.tcgetpgrp(fd) != os.getpgrp()
        ):
            raise ValueError("secret input requires a controlling TTY")
        original = termios.tcgetattr(fd)
        hidden = list(original)
        hidden[3] &= ~(termios.ECHO | getattr(termios, "ECHONL", 0))
        for name in ("SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT"):
            signum = getattr(signal, name, None)
            if signum is not None:
                handlers[signum] = signal.getsignal(signum)
                signal.signal(signum, interrupt)
        termios.tcsetattr(fd, termios.TCSAFLUSH, hidden)
        if termios.tcgetattr(fd)[3] & termios.ECHO:
            raise ValueError("controlling TTY echo could not be disabled")
        _write_tty(fd, prompt.encode("ascii"))
        prompted = True

        value = bytearray()
        oversized = False
        while True:
            byte = os.read(fd, 1)
            if not byte:
                raise ValueError("secret input was not completed")
            if byte in (b"\n", b"\r"):
                break
            if len(value) <= maximum:
                value.extend(byte)
            else:
                oversized = True
        if oversized or len(value) > maximum:
            raise ValueError("secret input is too long")
        try:
            decoded = value.decode("ascii")
        except UnicodeDecodeError as exc:
            raise ValueError("secret input is malformed") from exc
        if not decoded:
            raise ValueError("secret input is empty")
        return decoded
    except OSError as exc:
        if isinstance(exc, InterruptedError):
            raise
        raise ValueError("secret input requires an accessible controlling TTY") from exc
    finally:
        restore_error = None
        if fd >= 0 and original is not None:
            try:
                termios.tcsetattr(fd, termios.TCSANOW, original)
                restored = termios.tcgetattr(fd)
                echo_flags = termios.ECHO | getattr(termios, "ECHONL", 0)
                if restored[3] & echo_flags != original[3] & echo_flags:
                    raise ValueError("controlling TTY echo state was not restored")
                if prompted:
                    _write_tty(fd, b"\n")
            except (OSError, ValueError) as exc:
                restore_error = exc
        for signum, handler in handlers.items():
            signal.signal(signum, handler)
        if fd >= 0:
            os.close(fd)
        if restore_error is not None:
            raise ValueError("controlling TTY state could not be restored") from restore_error


def scan_artifacts(root, redaction_values_path):
    root = pathlib.Path(root)
    secrets = read_redaction_values(redaction_values_path)
    for path in root.rglob("*"):
        if path.is_symlink():
            raise ValueError(f"artifact symlink is not allowed: {path}")
        if not path.is_file():
            continue
        text = path.read_text(errors="ignore")
        if re.search(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----", text):
            raise ValueError(f"artifact contains private key material: {path}")
        if redact_text(text, secrets) != text:
            raise ValueError(f"artifact contains sensitive material: {path}")


def prepare_directory(path):
    path = _absolute(path, "second-factor handoff directory")
    if not path.exists():
        path.mkdir(mode=0o700)
    return validate_handoff_directory(path)


def stage_handoff(directory, operation, payload):
    directory = validate_handoff_directory(directory)
    if operation not in {"rotate", "reveal"}:
        raise ValueError("second-factor operation is invalid")
    _decode_handoff(json.dumps(payload).encode(), time.time)
    path = directory / f"{operation}.json"
    temporary = directory / f".{operation}.{os.getpid()}.{time.time_ns()}"
    _write_private_json(temporary, payload)
    try:
        os.link(temporary, path, follow_symlinks=False)
    except FileExistsError as exc:
        raise ValueError(f"{operation} second-factor handoff already exists") from exc
    finally:
        temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)

    preflight = sub.add_parser("preflight")
    preflight.add_argument("--directory", required=True)
    preflight.add_argument("--timeout", required=True, type=int)

    prepare = sub.add_parser("prepare")
    prepare.add_argument("--directory", required=True)

    for command in ("stage-totp", "stage-otp"):
        stage = sub.add_parser(command)
        stage.add_argument("--directory", required=True)
        stage.add_argument("--operation", required=True, choices=("rotate", "reveal"))

    consume = sub.add_parser("consume")
    consume.add_argument("--directory", required=True)
    consume.add_argument("--operation", required=True, choices=("rotate", "reveal"))
    consume.add_argument("--timeout", required=True, type=int)
    consume.add_argument("--output", required=True)
    consume.add_argument("--paired-output")
    consume.add_argument("--redaction-values", required=True)
    consume.add_argument("--service-id", required=True)
    consume.add_argument("--secret-name", required=True)
    consume.add_argument("--reject-fingerprint", default="")

    redact = sub.add_parser("redact")
    redact.add_argument("--redaction-values", required=True)
    redact_secret = sub.add_parser("redact-secret-response")
    redact_secret.add_argument("--redaction-values", required=True)
    scan = sub.add_parser("scan")
    scan.add_argument("--artifact-directory", required=True)
    scan.add_argument("--redaction-values", required=True)

    args = parser.parse_args()
    if args.command == "preflight":
        validate_handoff_directory(args.directory)
        validate_timeout(args.timeout)
    elif args.command == "prepare":
        prepare_directory(args.directory)
    elif args.command == "stage-totp":
        code = read_tty_secret("TOTP code: ", 6)
        now = int(time.time())
        stage_handoff(
            args.directory,
            args.operation,
            {
                "type": "totp",
                "code": code,
                "period_start_unix": now - now % TOTP_PERIOD_SECONDS,
            },
        )
    elif args.command == "stage-otp":
        request_id = read_tty_secret("OTP request ID: ", 128)
        code = read_tty_secret("OTP code: ", 6)
        stage_handoff(
            args.directory,
            args.operation,
            {
                "type": "otp",
                "request_id": request_id,
                "code": code,
            },
        )
    elif args.command == "consume":
        metadata = consume_handoff(
            args.directory,
            args.operation,
            args.timeout,
            args.output,
            args.redaction_values,
            args.service_id,
            args.secret_name,
            paired_output_path=args.paired_output,
            reject_fingerprint=args.reject_fingerprint,
        )
        print(json.dumps(metadata, separators=(",", ":"), sort_keys=True))
    elif args.command == "redact":
        sys.stdout.write(
            redact_text(sys.stdin.read(), read_redaction_values(args.redaction_values))
        )
    elif args.command == "redact-secret-response":
        raw = sys.stdin.buffer.read(MAX_SECRET_RESPONSE_SIZE + 1)
        sys.stdout.write(redact_secret_response(raw, args.redaction_values))
    elif args.command == "scan":
        scan_artifacts(args.artifact_directory, args.redaction_values)


if __name__ == "__main__":
    try:
        main()
    except (OSError, TimeoutError, ValueError) as exc:
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)

#!/usr/bin/env python3
import json
import os
import socket
import subprocess
import sys
import threading
import time
from dataclasses import dataclass

import gi

gi.require_version("Gtk", "3.0")
from gi.repository import Gio, GLib, Gtk, Pango


FIELD = "__GITSKIM_FIELD__"
RECORD = "__GITSKIM_COMMIT__"
EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
RUNTIME_DIR = os.environ.get("XDG_RUNTIME_DIR") or f"/tmp/gitskim-{os.getuid()}"
SOCKET_PATH = os.path.join(RUNTIME_DIR, "gitskim.sock")
IDLE_SECONDS = 12 * 60 * 60
FULL_FILE_LIMIT = 2 * 1024 * 1024
DIFF_OUTPUT_LIMIT = 8 * 1024 * 1024


class GitError(Exception):
    pass


@dataclass(frozen=True)
class HistoryRow:
    kind: str
    revision: str
    graph: str
    subject: str
    refs: str = ""


@dataclass(frozen=True)
class ChangedFile:
    status: str
    path: str
    old_path: str = ""

    @property
    def label(self):
        return f"{self.status:<4} {self.old_path + ' → ' if self.old_path else ''}{self.path}"


class Repository:
    def __init__(self, path=".", revision="HEAD"):
        self.path = self.run("rev-parse", "--show-toplevel", cwd=path).strip()
        self.revision = self.run("rev-parse", "--verify", f"{revision}^{{commit}}").strip()
        self.revision_arg = revision

    def run(self, *args, cwd=None, check=True):
        result = subprocess.run(
            ["git", "-C", cwd or getattr(self, "path", "."), *args],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        if check and result.returncode:
            raise GitError(result.stderr.strip() or f"git {' '.join(args)} failed")
        return result.stdout

    def run_limited(self, *args, limit, check=True):
        process = subprocess.Popen(
            ["git", "-C", self.path, *args],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        output = process.stdout.read(limit + 1)
        truncated = len(output) > limit
        if truncated:
            process.terminate()
        try:
            _remainder, error = process.communicate(timeout=2)
        except subprocess.TimeoutExpired:
            process.kill()
            _remainder, error = process.communicate()
        if check and process.returncode and not truncated:
            message = error.decode(errors="replace").strip()
            raise GitError(message or f"git {' '.join(args)} failed")
        return output[:limit].decode(errors="replace"), truncated

    def history(self, count, ignore_whitespace=True):
        fmt = f"{RECORD}%H{FIELD}%P{FIELD}%D{FIELD}%s"
        output = self.run(
            "log", "--graph", "--topo-order", f"-n{count}", f"--format={fmt}", self.revision_arg
        )
        rows = []
        for kind, subject in (("unstaged", "Unstaged changes"), ("staged", "Staged changes")):
            row = HistoryRow(kind, "", "○", subject)
            if self.changed_files(row, ignore_whitespace):
                rows.append(row)
        for line in output.splitlines():
            if RECORD not in line:
                rows.append(HistoryRow("connector", "", line.rstrip(), ""))
                continue
            graph, fields = line.split(RECORD, 1)
            revision, _parents, refs, subject = (fields.split(FIELD, 3) + ["", "", "", ""])[:4]
            rows.append(HistoryRow("commit", revision, graph.rstrip(), subject, refs.strip()))
        return rows

    def changed_files(self, row, ignore_whitespace=True):
        args = ["diff", "--name-status", "--find-renames", "--find-copies"]
        if ignore_whitespace:
            args.append("--ignore-all-space")
        if row.kind == "staged":
            args.append("--cached")
        elif row.kind == "commit":
            parents = self.run("show", "-s", "--format=%P", row.revision).split()
            args.extend([parents[0] if parents else EMPTY_TREE, row.revision])
        output = self.run(*args)
        files = []
        for line in output.splitlines():
            fields = line.split("\t")
            if len(fields) < 2:
                continue
            status = fields[0]
            if status[0] in "RC" and len(fields) > 2:
                files.append(ChangedFile(status, fields[2], fields[1]))
            else:
                files.append(ChangedFile(status, fields[1]))
        if row.kind == "unstaged":
            untracked = self.run("ls-files", "--others", "--exclude-standard").splitlines()
            files.extend(ChangedFile("??", path) for path in untracked)
        return [
            item
            for item in files
            if item.status == "??" or not ignore_whitespace or self.diff(row, item)
        ]

    def diff(self, row, changed_file, ignore_whitespace=True, full_file=False):
        if changed_file.status == "??":
            args = ["diff", "--no-index", "--no-color"]
            if ignore_whitespace:
                args.append("--ignore-all-space")
            if full_file:
                args.append("--unified=999999")
            args.extend(["--", "/dev/null", changed_file.path])
            output, truncated = self.run_limited(*args, limit=DIFF_OUTPUT_LIMIT, check=False)
            return self.finish_diff(output, truncated)
        args = [
            "diff",
            "--no-ext-diff",
            "--no-color",
            "--find-renames",
            "--find-copies",
        ]
        if ignore_whitespace:
            args.append("--ignore-all-space")
        if full_file:
            args.append("--unified=999999")
        if row.kind == "staged":
            args.append("--cached")
        elif row.kind == "commit":
            parents = self.run("show", "-s", "--format=%P", row.revision).split()
            args.extend([parents[0] if parents else EMPTY_TREE, row.revision])
        args.extend(["--", *filter(None, (changed_file.old_path, changed_file.path))])
        output, truncated = self.run_limited(*args, limit=DIFF_OUTPUT_LIMIT)
        return self.finish_diff(output, truncated)

    @staticmethod
    def finish_diff(output, truncated):
        if truncated:
            output += f"\n\n[Diff truncated at {DIFF_OUTPUT_LIMIT // 1024 // 1024} MiB]\n"
        return output

    def file_size(self, row, changed_file):
        if row.kind == "unstaged" and changed_file.status[0] != "D":
            try:
                return os.lstat(os.path.join(self.path, changed_file.path)).st_size
            except FileNotFoundError:
                pass
        revisions = []
        if row.kind == "unstaged":
            revisions = [f":{changed_file.path}", f"HEAD:{changed_file.path}"]
        elif row.kind == "staged":
            revisions = [f":{changed_file.path}", f"HEAD:{changed_file.path}"]
        else:
            revisions.append(f"{row.revision}:{changed_file.path}")
            parents = self.run("show", "-s", "--format=%P", row.revision).split()
            if parents:
                revisions.append(f"{parents[0]}:{changed_file.old_path or changed_file.path}")
        for revision in revisions:
            size = self.run("cat-file", "-s", revision, check=False).strip()
            if size.isdigit():
                return int(size)
        return 0


class ResidentServer:
    def __init__(self, application):
        self.application = application
        self.listener = socket.socket(socket.AF_UNIX)

    def start(self):
        os.makedirs(RUNTIME_DIR, mode=0o700, exist_ok=True)
        try:
            os.unlink(SOCKET_PATH)
        except FileNotFoundError:
            pass
        self.listener.bind(SOCKET_PATH)
        os.chmod(SOCKET_PATH, 0o600)
        self.listener.listen()
        threading.Thread(target=self.serve, daemon=True).start()

    def serve(self):
        while True:
            try:
                connection, _address = self.listener.accept()
                with connection:
                    request = json.loads(connection.recv(65536).decode())
                    with self.application.state_lock:
                        if self.application.busy:
                            connection.sendall(b"BUSY\n")
                            continue
                        self.application.busy = True
                    connection.sendall(b"OK\n")
                    GLib.idle_add(
                        self.application.open_repository,
                        request["path"],
                        request.get("revision", "HEAD"),
                    )
            except (OSError, ValueError, KeyError):
                if self.listener.fileno() == -1:
                    return

    def stop(self):
        if self.listener.fileno() != -1:
            self.listener.close()
        try:
            os.unlink(SOCKET_PATH)
        except FileNotFoundError:
            pass


class GitSkim(Gtk.Application):
    def __init__(self, repository, resident=False):
        super().__init__(
            application_id="dev.gitskim.GitSkim", flags=Gio.ApplicationFlags.NON_UNIQUE
        )
        self.repository = repository
        self.resident = resident
        self.busy = True
        self.idle_deadline = None
        self.state_lock = threading.Lock()
        self.server = ResidentServer(self) if resident else None
        self.history_limit = 10
        self.history_rows = []
        self.files = []
        self.current_row = None
        self.current_file = None
        self.history_store = Gtk.ListStore(str, str, str, str)
        self.file_store = Gtk.ListStore(str, str, str)
        self.connect("activate", self.on_activate)
        self.connect("shutdown", self.on_shutdown)

    def on_activate(self, _application):
        self.window = Gtk.ApplicationWindow(
            application=self, title=f"GitSkim — {os.path.basename(self.repository.path)}"
        )
        self.window.set_default_size(1200, 760)
        self.window.maximize()
        if self.resident:
            self.window.connect("delete-event", self.on_window_close)

        self.history_view = Gtk.TreeView(model=self.history_store, headers_visible=False)
        history_renderer = Gtk.CellRendererText(
            family="monospace", ellipsize=Pango.EllipsizeMode.END
        )
        history_column = Gtk.TreeViewColumn("History", history_renderer, text=0)
        history_column.set_expand(True)
        self.history_view.append_column(history_column)
        history_selection = self.history_view.get_selection()
        history_selection.set_select_function(
            lambda _selection, model, path, _selected: model[path][1] != "connector"
        )
        history_selection.connect("changed", self.on_history_selected)

        self.file_view = Gtk.TreeView(model=self.file_store, headers_visible=False)
        file_renderer = Gtk.CellRendererText(
            family="monospace", ellipsize=Pango.EllipsizeMode.MIDDLE
        )
        file_column = Gtk.TreeViewColumn("Files", file_renderer, text=0)
        file_column.set_expand(True)
        self.file_view.append_column(file_column)
        self.file_view.get_selection().connect("changed", self.on_file_selected)

        self.diff_buffer = Gtk.TextBuffer()
        self.diff_buffer.create_tag("added", background="#d7f5dd", foreground="#174d22")
        self.diff_buffer.create_tag("removed", background="#f9d7d9", foreground="#682126")
        self.diff_buffer.create_tag("hunk", foreground="#654a9b")
        self.diff_view = Gtk.TextView(
            buffer=self.diff_buffer, editable=False, cursor_visible=False, monospace=True
        )
        self.diff_view.set_wrap_mode(Gtk.WrapMode.NONE)

        self.whitespace_toggle = Gtk.CheckButton(label="Show whitespace changes")
        self.whitespace_toggle.set_tooltip_text("Off by default: diffs use git --ignore-all-space")
        self.whitespace_toggle.connect("toggled", self.on_whitespace_toggled)
        self.full_file_toggle = Gtk.CheckButton(label="Show full file")
        self.full_file_handler = self.full_file_toggle.connect(
            "toggled", self.on_full_file_toggled
        )
        load_button = Gtk.Button(label="Load 100 more")
        load_button.connect("clicked", self.on_load_more)

        graph_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4)
        graph_box.pack_start(self.scroller(self.history_view), True, True, 0)
        graph_box.pack_start(load_button, False, False, 0)
        left = Gtk.Paned.new(Gtk.Orientation.VERTICAL)
        left.pack1(graph_box, True, False)
        left.pack2(self.scroller(self.file_view), True, False)
        left.set_position(240)

        diff_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4)
        toolbar = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL)
        toolbar.pack_end(self.whitespace_toggle, False, False, 8)
        toolbar.pack_end(self.full_file_toggle, False, False, 0)
        diff_box.pack_start(toolbar, False, False, 4)
        diff_box.pack_start(self.scroller(self.diff_view), True, True, 0)
        main = Gtk.Paned.new(Gtk.Orientation.HORIZONTAL)
        main.pack1(left, False, False)
        main.pack2(diff_box, True, False)
        main.set_position(280)
        self.window.add(main)
        self.window.show_all()
        GLib.idle_add(self.load_history)
        if self.server:
            self.hold()
            self.server.start()
            GLib.timeout_add_seconds(60, self.expire_if_idle)

    def open_repository(self, path, revision):
        try:
            self.repository = Repository(path, revision)
        except (GitError, FileNotFoundError) as error:
            self.show_error(str(error))
            with self.state_lock:
                self.busy = False
                self.idle_deadline = time.monotonic() + IDLE_SECONDS
            return False
        self.history_limit = 10
        self.clear_repository_view()
        self.whitespace_toggle.set_active(False)
        self.window.set_title(f"GitSkim — {os.path.basename(self.repository.path)}")
        self.window.show_all()
        self.window.maximize()
        self.window.present()
        self.load_history()
        return False

    def on_window_close(self, _window, _event):
        self.clear_repository_view()
        self.window.hide()
        with self.state_lock:
            self.busy = False
            self.idle_deadline = time.monotonic() + IDLE_SECONDS
        return True

    def clear_repository_view(self):
        self.history_rows = []
        self.files = []
        self.current_row = None
        self.current_file = None
        self.history_store.clear()
        self.file_store.clear()
        self.diff_buffer.set_text("")
        self.reset_full_file()

    def expire_if_idle(self):
        with self.state_lock:
            expired = not self.busy and time.monotonic() >= (self.idle_deadline or float("inf"))
        if expired:
            self.server.stop()
            self.release()
            self.quit()
            return False
        return True

    def on_shutdown(self, _application):
        if self.server:
            self.server.stop()

    @staticmethod
    def scroller(child):
        scroll = Gtk.ScrolledWindow()
        scroll.set_policy(Gtk.PolicyType.AUTOMATIC, Gtk.PolicyType.AUTOMATIC)
        scroll.add(child)
        return scroll

    def load_history(self, preferred_kind=None, preferred_revision=None):
        try:
            self.history_rows = self.repository.history(
                self.history_limit, not self.whitespace_toggle.get_active()
            )
        except GitError as error:
            self.show_error(str(error))
            return False
        self.history_store.clear()
        target = None
        for index, row in enumerate(self.history_rows):
            refs = f" ({row.refs})" if row.refs else ""
            label = f"{row.graph:<8} {row.subject}{refs}".rstrip()
            self.history_store.append([label, row.kind, row.revision, str(index)])
            if target is None and row.kind != "connector":
                target = index
            if preferred_kind == row.kind and (
                not preferred_revision or preferred_revision == row.revision
            ):
                target = index
                preferred_kind = None
        if target is not None:
            self.history_view.set_cursor(Gtk.TreePath.new_from_indices([target]))
        return False

    def on_history_selected(self, selection):
        model, tree_iter = selection.get_selected()
        if tree_iter is None:
            return
        index = int(model[tree_iter][3])
        if index >= len(self.history_rows):
            return
        row = self.history_rows[index]
        if row.kind == "connector":
            return
        self.reset_full_file()
        self.diff_buffer.set_text("")
        previous_path = self.current_file.path if self.current_file else None
        self.current_row = row
        try:
            self.files = self.repository.changed_files(row, not self.whitespace_toggle.get_active())
        except GitError as error:
            self.show_error(str(error))
            return
        self.file_store.clear()
        selected = None
        for index, changed_file in enumerate(self.files):
            self.file_store.append([changed_file.label, changed_file.path, str(index)])
            if changed_file.path == previous_path:
                selected = index
        if self.files:
            self.file_view.set_cursor(Gtk.TreePath.new_from_indices([selected or 0]))
        else:
            self.current_file = None
            self.set_diff("")

    def on_file_selected(self, selection):
        model, tree_iter = selection.get_selected()
        if tree_iter is None or self.current_row is None:
            return
        index = int(model[tree_iter][2])
        if index >= len(self.files):
            return
        changed_file = self.files[index]
        if changed_file != self.current_file:
            self.reset_full_file()
        self.current_file = changed_file
        size = self.repository.file_size(self.current_row, self.current_file)
        full_file_allowed = size <= FULL_FILE_LIMIT
        self.full_file_toggle.handler_block(self.full_file_handler)
        self.full_file_toggle.set_sensitive(full_file_allowed)
        if not full_file_allowed:
            self.full_file_toggle.set_active(False)
        self.full_file_toggle.handler_unblock(self.full_file_handler)
        self.full_file_toggle.set_tooltip_text(
            None
            if full_file_allowed
            else f"Disabled for files larger than {FULL_FILE_LIMIT // 1024 // 1024} MiB"
        )
        self.diff_buffer.set_text("")
        try:
            patch = self.repository.diff(
                self.current_row,
                self.current_file,
                not self.whitespace_toggle.get_active(),
                self.full_file_toggle.get_active(),
            )
        except GitError as error:
            self.show_error(str(error))
            return
        self.set_diff(patch)

    def set_diff(self, patch):
        self.diff_buffer.set_text("")
        in_header = True
        for line in patch.splitlines(keepends=True):
            if line.startswith("@@"):
                in_header = False
            elif in_header and line.startswith(("diff --git ", "index ", "--- ", "+++ ")):
                continue
            end = self.diff_buffer.get_end_iter()
            tag = None
            if line.startswith("@@"):
                tag = "hunk"
            elif line.startswith("+") and not line.startswith("+++"):
                tag = "added"
            elif line.startswith("-") and not line.startswith("---"):
                tag = "removed"
            if tag:
                self.diff_buffer.insert_with_tags_by_name(end, line, tag)
            else:
                self.diff_buffer.insert(end, line)

    def on_whitespace_toggled(self, _button):
        if self.current_row:
            row = self.current_row
            self.load_history(row.kind, row.revision)

    def on_full_file_toggled(self, _button):
        if self.current_file:
            self.on_file_selected(self.file_view.get_selection())

    def reset_full_file(self):
        self.full_file_toggle.handler_block(self.full_file_handler)
        self.full_file_toggle.set_active(False)
        self.full_file_toggle.handler_unblock(self.full_file_handler)

    def on_load_more(self, _button):
        self.history_limit += 100
        row = self.current_row
        self.load_history(row.kind if row else None, row.revision if row else None)

    def show_error(self, message):
        dialog = Gtk.MessageDialog(
            transient_for=getattr(self, "window", None),
            flags=0,
            message_type=Gtk.MessageType.ERROR,
            buttons=Gtk.ButtonsType.CLOSE,
            text="GitSkim could not load the repository",
        )
        dialog.format_secondary_text(message)
        dialog.run()
        dialog.destroy()


def main(argv=None):
    argv = list(sys.argv if argv is None else argv)
    resident = len(argv) > 1 and argv[1] == "--resident"
    internal = resident or (len(argv) > 1 and argv[1] == "--ephemeral")
    if internal:
        if len(argv) != 4:
            print(f"usage: {argv[0]} [--resident|--ephemeral] path revision", file=sys.stderr)
            return 2
        path, revision = argv[2:]
    else:
        path, revision = ".", argv[1] if len(argv) > 1 else "HEAD"
    if not internal and len(argv) > 2:
        print(f"usage: {argv[0]} [HEAD|branch|tag|sha]", file=sys.stderr)
        return 2
    try:
        repository = Repository(path, revision)
    except (GitError, FileNotFoundError) as error:
        print(f"gitskim: {error}", file=sys.stderr)
        return 1
    return GitSkim(repository, resident).run([argv[0]])


if __name__ == "__main__":
    raise SystemExit(main())

# File explorer

The **Fichiers** view (`/files`) browses the filesystem of the host that serves the web UI. It opens text files in a built-in editor, renders Markdown, previews images, video and audio, and transfers files in both directions.

## Access

- The explorer reaches what the TmuxAtlas process can reach: the operating system enforces permissions. A file the process cannot write opens read-only.
- With account authentication, only `admin` accounts can use it. Accounts with the `user` role get `403` on every `/api/files/*` route and do not see the view.
- Every write (save, upload, create, rename, move, delete) needs a same-origin request, plus the CSRF token when authentication is on.
- Set `TMUXATLAS_FILES=off` to disable the explorer: its routes then return `404`.
- Remote peers are not browsed: the explorer only shows the local host.

## Editing

- Text files up to 5 MiB open in the editor. Larger files and binary files show their details and a download button.
- Saving checks the version the file had when it was opened. If something else changed the file in the meantime, the editor asks whether to overwrite it or reload it.
- Saves are atomic (temporary file, then rename) and keep the mode and owner. Hard-linked files, and files whose owner cannot be preserved, are written in place. Saving through a symlink writes the target and keeps the link.
- CRLF line endings are preserved.

## Previews

Images (PNG, JPEG, GIF, WebP, AVIF, BMP, ICO, SVG, TIFF), video and audio are served inline by `/api/files/raw`. Every other type is served as an attachment. All raw responses carry `Content-Security-Policy: sandbox`, so an SVG or HTML file never runs scripts on the TmuxAtlas origin.

## Transfers

- Drop files or folders from the desktop on the view to upload them to the current folder, or on a folder of the list to upload them there. Folder structure is kept, empty folders included.
- Uploads are written to a temporary file first and never replace an existing file without asking.
- One upload is limited to 4 GiB. Set `TMUXATLAS_FILES_UPLOAD_MAX` (in megabytes) to change the limit. A reverse proxy in front of TmuxAtlas may enforce its own body limit, such as nginx `client_max_body_size`.
- Drag an item from the list to the desktop to download it (Chromium browsers). A folder downloads as a `.zip`. Every item also has a **Télécharger** action.
- Drag an item onto a folder or a breadcrumb segment to move it.

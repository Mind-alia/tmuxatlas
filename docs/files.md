# Navigateur de fichiers

La page `/files` parcourt les fichiers d’un compte Unix : celui du hub, celui
d’`agents`, ou celui d’une personne invitée. Le sélecteur « Compte » liste les
hôtes que le rôle autorise : tous pour un administrateur, ceux qui lui sont
attribués pour un invité (jamais l’hôte du hub, sauf
`TMUXATLAS_GUEST_LOCAL_HOST=allow`).

## Qui lit les fichiers

Les fichiers d’un compte ne sont lus et écrits que par un processus qui tourne
sous ce compte :

- l’hôte du hub, par le hub lui-même ;
- tout autre hôte, par son `tmuxatlas agent`, au moyen des requêtes `fs.*` du
  protocole (capacité `files-v1`).

Si l’Agent d’un hôte est hors ligne, l’API répond 503 : le hub ne lit jamais
les fichiers d’un compte avec les droits d’un autre.

## Plafond local

Chaque processus fixe lui-même ce qu’il accepte, dans son environnement
(`~/.config/tmuxatlas/.env` de son compte) ; le hub ne peut pas le dépasser :

| Variable | Défaut | Rôle |
|----------|--------|------|
| `TMUXATLAS_FILES` | `off` | `off`, `ro` (lecture) ou `rw` (lecture et écriture) |
| `TMUXATLAS_FILES_ROOT` | `$HOME` | Racine du jail |
| `TMUXATLAS_FILES_DENY` | — | Motifs à refuser en plus, séparés par des virgules |

Recommandation : `ro` (ou `off`) pour le compte de production. Le changement
prend effet au redémarrage du processus.

## Protections

- Jail `os.Root` à la racine ; `..` ne remonte pas au-dessus.
- Aucun lien symbolique n’est suivi, ni en fin de chemin ni au milieu.
- Seuls les fichiers ordinaires et les dossiers sont servis.
- Jamais listés ni servis : `.ssh`, `.gnupg`, `.config/tmuxatlas`, `.env*`,
  `*.pem`, `*.key`, `.docker`, `.netrc`, `.git-credentials`, `.pgpass`, `.aws`,
  `.kube`, `.password-store`, `.local/share/keyrings`.
- Écriture dans un fichier temporaire privé, puis `rename` atomique. Le
  fichier ouvert porte une version (date et taille) : enregistrer après une
  modification extérieure répond 409.
- Téléchargements toujours en pièce jointe, `application/octet-stream`,
  `nosniff` et CSP `sandbox`.
- Chaque écriture est journalisée par le processus du compte, avec le nom du
  compte Atlas qui l’a demandée.

## Limites

| Élément | Limite |
|---------|--------|
| Ouvrir un fichier texte | 1 Mio |
| Télécharger | 8 Mio |
| Envoyer | 1 Mio par fichier |
| Lister | 2000 entrées |
| Opérations simultanées | 4 par compte Atlas, 2 par processus servant |

La suppression d’un dossier n’accepte qu’un dossier vide.

## API

Toutes les routes prennent `host_id` dans la requête. Les mutations exigent
`Origin` et `X-TmuxAtlas-CSRF`. Les erreurs ont la forme
`{"code":"…","message":"…"}`.

| Route | Rôle |
|-------|------|
| `GET /api/files/hosts` | Comptes que le rôle autorise : `{"hosts":[{"id","name","online","local"}]}` |
| `GET /api/files/info` | `{"mode","root"}` du compte choisi |
| `GET /api/files/list?path=` | `{"path","entries":[{"name","type","size","mtime","mode"}],"truncated"}` |
| `GET /api/files/content?path=` | Fichier texte UTF-8 : `{"path","content","size","mtime","version","writable"}` ; 415 pour un fichier binaire |
| `PUT /api/files/content?path=&version=` | Corps texte ; enregistre si la version n’a pas changé |
| `GET /api/files/raw?path=` | Téléchargement |
| `POST /api/files/upload?path=&overwrite=1` | Corps `application/octet-stream` |
| `POST /api/files/mkdir` | `{"path"}` |
| `POST /api/files/rename` | `{"from","to"}` ; refuse d’écraser |
| `POST /api/files/delete` | `{"path"}` |

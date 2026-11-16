# Déploiement du hub TmuxAtlas

Le hub tourne sous `transcriptflow`, qui appartient aux groupes `docker` et
`sudo`. Tout ce qui est compilé ou copié pour lui finit donc exécuté avec les
droits de root. Le compte `agents` fait tourner des agents autonomes, à
considérer comme hostiles : **rien de ce qui est déployé ne doit venir d'un
fichier qu'il peut écrire**.

Règles :

- `transcriptflow` compile depuis **son propre clone** du fork, dans son
  répertoire personnel. On ne lit, ne copie et n'exécute rien dans
  `/home/agents` (ni worktree, ni binaire `dist/`, ni cache Go ou npm, ni
  script `~/projets/outils`).
- On déploie **un commit précis**, relu sur GitHub, dont on a noté le SHA
  complet (40 caractères) hors du serveur, par exemple dans la PR fusionnée.
- Le binaire installé appartient à root, dans `/opt/tmuxatlas`. Les comptes
  invités utilisent ce même binaire (voir la fenêtre « Accès Linux » du
  panneau Comptes).
- On garde le binaire précédent pour revenir en arrière en une commande.

## 0. Une fois : le clone et le dossier d'installation

```bash
# En transcriptflow
install -d -m 700 ~/src
git clone https://github.com/Mind-alia/tmuxatlas.git ~/src/tmuxatlas

# En root (transcriptflow a sudo) : le dossier que seul root peut écrire
sudo install -d -o root -g root -m 755 /opt/tmuxatlas
```

## 1. Récupérer et vérifier le commit attendu

```bash
cd ~/src/tmuxatlas
EXPECTED_SHA=<sha complet du commit relu>     # copié depuis la PR, pas depuis le serveur
git fetch --prune origin
git cat-file -e "$EXPECTED_SHA^{commit}" || { echo "commit inconnu"; exit 1; }
git merge-base --is-ancestor "$EXPECTED_SHA" origin/main || { echo "pas sur main"; exit 1; }
git checkout --detach "$EXPECTED_SHA"
test "$(git rev-parse HEAD)" = "$EXPECTED_SHA" || { echo "mauvais commit"; exit 1; }
test -z "$(git status --porcelain --ignored)" || { echo "clone pas propre : git clean -fdx"; exit 1; }
```

`git status --ignored` doit être vide : un `node_modules` ou un `pkg/server/dist`
laissé par un ancien build serait embarqué tel quel.

## 2. Relire le diff depuis la version en place

```bash
DEPLOYED_SHA=$(cat /opt/tmuxatlas/DEPLOYED_SHA 2>/dev/null || echo "")
git log --oneline "${DEPLOYED_SHA:-$EXPECTED_SHA~1}..$EXPECTED_SHA"
git diff --stat "${DEPLOYED_SHA:-$EXPECTED_SHA~1}" "$EXPECTED_SHA"
git diff "${DEPLOYED_SHA:-$EXPECTED_SHA~1}" "$EXPECTED_SHA" -- go.mod go.sum web/package-lock.json contrib/ install.sh Makefile
```

Regarder en particulier : nouvelles dépendances (`go.mod`, `go.sum`,
`web/package-lock.json`), scripts (`contrib/`, `Makefile`, `install.sh`,
`scripts`), et tout ce qui touche à `pkg/auth`, `pkg/sudo*`, `pkg/files`,
`pkg/server`. Si le diff contient autre chose que ce qui a été relu dans la
PR, on s'arrête.

## 3. Compiler

```bash
cd ~/src/tmuxatlas
export GOTOOLCHAIN=local GOFLAGS=-mod=readonly
go mod verify
(cd web && npm ci --ignore-scripts --no-audit --no-fund && npm run build)
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X github.com/LosFurina/tmuxatlas/pkg/common.COMMIT=$EXPECTED_SHA" \
  -o dist/tmuxatlas .
./dist/tmuxatlas --version
sha256sum dist/tmuxatlas
```

Go n'est pas installé pour `transcriptflow` sur le serveur : on compile le
binaire dans l'image `golang:1.25-bookworm` (celle des binaires précédents),
avec les caches de `transcriptflow`, après avoir construit le frontend avec le
`npm` de l'hôte comme ci-dessus :

```bash
docker run --rm --user "$(id -u):$(id -g)" -v ~/src/tmuxatlas:/src -w /src \
  -v ~/go/pkg/mod:/gomod -v ~/.cache/go-build:/gocache \
  -e HOME=/tmp -e GOMODCACHE=/gomod -e GOCACHE=/gocache \
  -e GOTOOLCHAIN=local -e GOFLAGS=-mod=readonly -e CGO_ENABLED=0 \
  golang:1.25-bookworm sh -c "go mod verify && go build -trimpath \
    -ldflags '-s -w -X github.com/LosFurina/tmuxatlas/pkg/common.COMMIT=$EXPECTED_SHA' \
    -o dist/tmuxatlas . && ./dist/tmuxatlas --version"
```

Les caches (`~/go`, `~/.cache/go-build`, `~/.npm`) sont ceux de
`transcriptflow`. Ne pointez jamais `GOCACHE`, `GOMODCACHE`, `GOPATH` ou
`npm_config_cache` vers `/home/agents`.

Lancer les tests ciblés si le temps le permet (`go test ./pkg/...`), avec
`TMUX` vidé et `TMUX_TMPDIR` temporaire (voir `CLAUDE.md`). Dans un conteneur,
il faut `tmux` dans l'image et un `/etc/passwd` qui nomme l'uid utilisé, sinon
`TestSudoTerminalEndToEnd` expire faute de nom d'utilisateur ;
`TestSessionNamedAsIdCannotReachAnotherSession` échoue avec le tmux 3.3a de
Debian bookworm, comme sur les versions précédentes.

## 4. Installer en gardant le binaire précédent

```bash
sudo install -o root -g root -m 755 /opt/tmuxatlas/tmuxatlas /opt/tmuxatlas/tmuxatlas.previous 2>/dev/null || true
sudo cp -p /opt/tmuxatlas/DEPLOYED_SHA /opt/tmuxatlas/DEPLOYED_SHA.previous 2>/dev/null || true
sudo install -o root -g root -m 755 dist/tmuxatlas /opt/tmuxatlas/tmuxatlas
echo "$EXPECTED_SHA" | sudo tee /opt/tmuxatlas/DEPLOYED_SHA >/dev/null
stat -c '%U:%G %a %n' /opt/tmuxatlas /opt/tmuxatlas/tmuxatlas   # root:root 755
```

`install` remplace le fichier sans écrire dans l'ancien : les processus en
cours gardent l'ancienne version jusqu'à leur redémarrage.

La première fois seulement, faire pointer le service du hub sur ce binaire :
`/opt/tmuxatlas/tmuxatlas install --mode server --public-url http://localhost:7654`
(le service reprend le chemin du binaire qui l'installe). **Pas `--mode hub`** :
ce mode est un hub distant seulement, sans tmux ni fichiers locaux, et les
sessions de `transcriptflow` disparaîtraient de l'interface. Vérifier ensuite
que l'unité contient `KillMode=process` (`systemctl --user cat
tmuxatlas.service`) : sans lui, le redémarrage tue le serveur tmux de
`transcriptflow` et toutes ses sessions.

Avant de redémarrer, ajouter au `.env` du hub les variables nouvelles de la
version (voir le `CHANGELOG.md` et les notes de passation). Les secrets vont
dans `~/.config/tmuxatlas/.env` de `transcriptflow`, en `chmod 600`, jamais
dans git.

## 5. Redémarrer et vérifier

```bash
systemctl --user restart tmuxatlas.service
systemctl --user status tmuxatlas.service --no-pager
journalctl --user -u tmuxatlas.service -n 50 --no-pager
/opt/tmuxatlas/tmuxatlas healthcheck   # puis ouvrir l'interface (tunnel SSH, ou le proxy s'il y en a un)
```

Les peers invités utilisent aussi `/opt/tmuxatlas/tmuxatlas` : chacun reprend
la nouvelle version à son prochain redémarrage
(`systemctl --user restart tmuxatlas-agent.service` sous son compte).

## 6. Retour arrière

```bash
sudo install -o root -g root -m 755 /opt/tmuxatlas/tmuxatlas.previous /opt/tmuxatlas/tmuxatlas
sudo cp -p /opt/tmuxatlas/DEPLOYED_SHA.previous /opt/tmuxatlas/DEPLOYED_SHA
systemctl --user restart tmuxatlas.service
```

Si la nouvelle version a ajouté des variables au `.env`, l'ancienne les ignore :
on peut les laisser. Si elle a modifié des fichiers d'état
(`~/.local/state/tmuxatlas`), sauvegardez ce dossier avant l'étape 5 pour
pouvoir le restaurer.

## Particularités du serveur de prod

- **Pas de Caddy aujourd'hui** : l'interface n'est joignable que par un tunnel
  SSH vers `127.0.0.1:7654`. `TMUXATLAS_PROXY_SECRET` n'est lu qu'en mode
  comptes (`TMUXATLAS_AUTH=accounts`) et ne sert que derrière un proxy qui
  ajoute l'en-tête `X-Tmuxatlas-Proxy-Secret` : sans proxy, il n'y a pas de
  `header_up` à configurer.
- **Terminal sudo** : `TMUXATLAS_SUDO_REQUESTERS` (empreinte du peer agents)
  laisse les agents déposer des demandes, mais aucun terminal ne s'ouvre sans
  un peer `tmuxatlas agent --sudo-terminal` sous un compte admin, épinglé par
  `TMUXATLAS_SUDO_PEERS` (voir [sudo-terminal.md](sudo-terminal.md)).
- **Collecteur ops** : le hub tourne sous `transcriptflow`, le compte qui
  possède Docker. Le dossier d'état peut donc rester dans son home
  (`TMUXATLAS_OPS_DIR=~/.local/state/tf-ops`, et
  `ReadWritePaths=%h/.local/state/tf-ops` dans l'unité du collecteur), sans
  groupe `tfops` ni `/srv/tf-ops`.

## Scripts `contrib/ops`

Ils se copient depuis ce même clone, au commit vérifié à l'étape 1 : voir
[ops.md](ops.md#installation-by-a-human-on-the-production-side).

# Terminal sudo

Un agent (Claude Code) n'a pas de TTY et ne peut pas taper de mot de passe. Quand
il a besoin de `sudo`, il **demande** : la demande apparaît dans le cockpit de sa
session, un administrateur peut la modifier, l'approuver, taper lui-même le mot
de passe dans un terminal éphémère, puis relire ce qui sera renvoyé à l'agent.

Deux garanties :

1. **Rien ne s'exécute sans l'humain.** Seule une version exacte de la commande,
   approuvée par un administrateur, est lancée, et `sudo` exige le mot de passe
   tapé en direct (`sudo -k`, refus du `NOPASSWD`).
2. **L'agent ne voit jamais le mot de passe.** Il ne reçoit que le résultat que
   l'humain a relu, capturé par des pipes où le mot de passe ne passe pas.

## Topologie visée

| Compte Unix | Processus | Rôle |
|---|---|---|
| `transcriptflow` | `tmuxatlas server` (hub, port 7654) | Stocke les demandes, sert l'interface, relaie les octets du terminal. |
| `agents` | `tmuxatlas agent` (peer) + les Claude autonomes | Émet les demandes. **Aucun droit sudo.** |
| compte de l'admin (`anovisclaw`, `ubuntu` ou `transcriptflow`) | `tmuxatlas agent --sudo-terminal` (peer sudo) | Seul à exécuter `sudo`, dans un PTY éphémère, sous son propre compte. |

Les membres du groupe sudo sont `transcriptflow`, `anovisclaw` et `ubuntu`. Le
peer sudo ne tourne **jamais** sous `agents`, ni sous root (il refuse de démarrer
en uid 0).

```
Claude (agents) ──CLI──▶ socket local du peer agents ──WS /ws/peer──▶ hub (transcriptflow)
                                                                       │  store pkg/sudo
navigateur admin ◀──/api/sudo, /ws/sudo──▶ hub ──sudo-open──▶ peer sudo (compte admin)
                                              ◀─/ws/peer-pty──  PTY : sudo -k -- sh -c "<cmd>"
```

## Cycle de vie (`pkg/sudo`)

```
pending ──edit (v+1)──▶ pending
pending ──approve(version exacte, peer)──▶ approved  (grant à usage unique, 60 s)
approved ──grant non utilisé──▶ pending
approved ──redeem (même admin)──▶ running
running ──capture──▶ awaiting_review
awaiting_review ──Envoyer / Code de sortie seul──▶ delivered
awaiting_review ──Rien──▶ withheld
awaiting_review ──Modifier et relancer (v+1)──▶ pending   (nouvelle approbation)
Autres fins : refused, cancelled (par l'agent, avant l'exécution), expired, failed
```

Durées (défauts, `sudo.DefaultLimits`) :

| Étape | Durée |
|---|---|
| Demande en attente / approuvée | 15 min après création, puis `expired` |
| Grant (ouvrir le terminal) | 60 s, puis retour en `pending` |
| Terminal (côté peer) | 15 min au total, 5 min sans frappe ni sortie |
| Terminal (plafond vu du hub) | 16 min, puis `failed` |
| Relecture | 30 min, puis `withheld` (rien ne part) |
| Sortie capturée | 64 Kio, puis tronquée |

Une commande est sur **une seule ligne** : un saut de ligne permettrait de cacher
une seconde commande sous celle qu'on lit. Les séquences de contrôle sont refusées.

## Côté agent

```bash
tmuxatlas sudo-request --reason "installer jq" -- "apt-get install -y jq"
```

- Passe par le socket Unix local (celui du `tmuxatlas agent` du compte `agents`),
  qui transmet au hub par le canal de contrôle déjà authentifié (Ed25519).
- Long-poll : attend 8 min par défaut (`--timeout`), sous la limite de 10 min du
  Bash tool. Au-delà : code de sortie **75** et
  `tmuxatlas sudo-request --wait <id>` pour reprendre.
- Codes de sortie : 0 = résultat livré (le code de la commande est dans le JSON),
  1 = refusé, retenu, expiré, annulé ou échoué, 75 = toujours en attente,
  2 = erreur d'usage ou de transport.
- Sortie : `{id, status, command_requested, command_executed, modified,
  exit_code, output, output_truncated, note}`.
- Ctrl-C (ou `--cancel <id>`) annule une demande qui n'a pas encore démarré.

Le rattachement à la conversation reprend `TMUX_PANE` (session, fenêtre, pane),
le `cwd`, le pid et, si Claude l'exporte, `CLAUDE_SESSION_ID`. Ces champs sont
déclarés par l'agent : l'interface les montre comme tels. Seul l'hôte d'origine
est établi par le hub (empreinte du peer authentifié).

### Hook PreToolUse

`tmuxatlas agent-setup --sudo-hook` ajoute un hook `PreToolUse` (matcher `Bash`)
qui appelle `tmuxatlas sudo-hook`. Si la commande appelle `sudo`, le hook refuse
l'appel (`permissionDecision: deny`) avec un message qui renvoie vers
`tmuxatlas sudo-request`. Il ne crée pas de demande lui-même.

Le hook est une **aide ergonomique**, pas une frontière : le compte `agents` n'a
aucun droit sudo, c'est ça la frontière. `agent-setup` **fusionne** désormais ses
hooks avec ceux déjà présents dans `settings.json` au lieu de les écraser.

## Notification

Le hub enregistre un événement `toolevents` `tool: "sudo"`, `status: "waiting"`
sur le pane d'origine. La barre latérale, le badge et le webpush existants le
prennent en charge. Le message reste générique : la commande n'est visible que
par les administrateurs, via `/api/sudo`. L'événement disparaît quand la demande
se termine.

## Côté administrateur

API navigateur, toutes réservées aux administrateurs (cookie + `Origin` +
CSRF pour les mutations, plus le contrôle « est admin ») :

| Route | Rôle |
|---|---|
| `GET /api/sudo/requests` | Liste |
| `GET /api/sudo/requests/{id}` | Détail, avec la capture si elle attend une relecture |
| `POST /api/sudo/requests/{id}/edit` | `{version, command}` |
| `POST /api/sudo/requests/{id}/refuse` | `{note}` |
| `POST /api/sudo/requests/{id}/approve` | `{version, peer}` → `{grant, expires_at}` |
| `POST /api/sudo/requests/{id}/review` | `{decision: send, exit-code, nothing, note}` |
| `POST /api/sudo/requests/{id}/rerun` | `{version, command}` |
| `GET /api/sudo/peers` | Peers sudo disponibles |
| `GET /ws/sudo` | Terminal ; le grant voyage dans la 1re trame, jamais dans l'URL |

« Est admin » passe par une petite interface (`server.SudoAdmins`) : en mode
comptes, c'est le rôle `admin` du compte ; en mode passkey (un seul
propriétaire), toute session valide ; sans authentification, le terminal sudo
est **désactivé**. Le modèle de comptes n'est pas modifié.

`/ws/sudo` exige un en-tête `Origin` égal à l'URL publique (l'ancien
`CheckSameOrigin` acceptait l'absence d'`Origin`).

Le panneau Sudo s'ouvre sous le cockpit (`TerminalCockpit`) de la session
concernée, pour les administrateurs (il interroge `/api/sudo` toutes les 3 s et
se masque si le serveur refuse) :

1. **Demande** : raison, origine, commande éditable (versionnée, l'original reste
   affiché), choix du peer sudo, Approuver / Refuser.
2. **Terminal** : xterm dédié, sans brouillons, sans `localStorage`, sans
   composeur mobile. Un bandeau signale la saisie masquée.
3. **Relecture** : sortie caviardée et code de sortie. Envoyer / Code de sortie
   seul / Rien, ou Modifier et relancer.

## Côté peer sudo (`pkg/sudoterm`)

Activé explicitement : `tmuxatlas agent --sudo-terminal` (ou
`TMUXATLAS_SUDO_TERMINAL=1`). Le hub n'ouvre de terminal que vers un peer
**épinglé** par `TMUXATLAS_SUDO_PEERS` (ou `--sudo-peers`) : des **empreintes**
séparées par des virgules, jamais des noms (un nom n'est qu'une étiquette).
Le peer doit être connecté, annoncer la capacité `sudo-terminal`, et être
différent de l'hôte d'origine de la demande. Sans cet
épinglage, un agent hostile qui relancerait son propre peer avec
`--sudo-terminal` recevrait le mot de passe de l'admin.

Pour chaque ouverture :

1. Contrôle `NOPASSWD` : `sudo -k` puis `sudo -n true` sans terminal. Si ça
   réussit, aucun mot de passe ne serait demandé : **refus**.
2. PTY éphémère, hors tmux, environnement assaini (`HISTFILE=/dev/null`, pas de
   `SSH_AUTH_SOCK`, `PATH` fixe), nouvelle session (`setsid`).
3. `sudo -k -p "[sudo] mot de passe de %p : " -- /bin/sh -c "<cmd>"` :
   - stdin et le terminal de contrôle sont le PTY : `sudo` lit le mot de passe
     sur `/dev/tty` ;
   - **stdout et stderr sont des pipes**, recopiés à l'écran et dans une capture
     de 64 Kio. Le mot de passe ne passe jamais par ces pipes.
4. Les frappes de l'humain sont gardées en mémoire le temps de la commande, puis
   chaque ligne de 4 caractères ou plus est **caviardée** de la capture (au cas
   où un programme l'aurait recopiée), et le tampon est effacé.
5. Avant que `sudo` n'affiche son invite, le terminal imprime la **commande
   exacte**, sa longueur et les 12 premiers caractères de son sha256 : les mêmes
   que dans le panneau. On ne tape le mot de passe qu'après avoir vérifié
   qu'ils correspondent.
6. L'état de l'écho du PTY est surveillé : quand il est coupé, le navigateur
   affiche « saisie masquée ».
7. Fin : kill du groupe de processus, puis `sudo -k`.
8. La capture part vers le hub en `awaiting_review` : l'agent ne reçoit rien
   tant que l'humain n'a pas relu.

Audit faisant foi : `$XDG_STATE_HOME/tmuxatlas/sudo-audit.jsonl` (défaut
`~/.local/state/tmuxatlas/`, mode 0600) chez le compte de l'admin. Une ligne par
ouverture, refus ou fin : demande, version, commande et son empreinte sha256,
approbateur, origine, code de sortie, durée, taille de sortie. **Jamais les
frappes ni la sortie.** `tmuxatlas sudo-audit` l'affiche. Complément : journald et
`auth.log` de sudo.

## Frappes vers le terminal d'un agent

**La règle est simple : on ne tape jamais de mot de passe dans le terminal
d'un hôte qui n'est ni le hub ni un peer sudo épinglé.** Ces terminaux portent
en permanence le bandeau « Hôte non fiable : ne tapez jamais de mot de passe
ici » (liste servie par `GET /api/hosts/trust`, et bandeau affiché tant que la
réponse manque). Le seul endroit où taper un mot de passe sudo est le terminal
du panneau Sudo.

Le blocage décrit ci-dessous n'est **qu'une aide**, pas une garantie : la
détection tourne dans le peer de l'hôte surveillé, c'est-à-dire sous le
contrôle de l'agent, et une fausse invite affichée par un programme en mode
brut (sans ICANON) n'est pas détectée.

Un agent pourrait imiter une invite `[sudo] password` dans son propre pane pour
récupérer un mot de passe tapé par erreur. Le peer qui sert un terminal tmux
surveille (toutes les 500 ms) le TTY du pane actif : quand il est en mode
canonique **sans écho** (la signature d'une invite de mot de passe, contrairement
aux TUI comme Claude Code qui sont en mode brut), il envoie une trame `echo off`,
et le navigateur bloque les frappes, les collages et le composeur vers ce
terminal en affichant un avertissement qui renvoie vers le panneau Sudo. Un clic
explicite « Taper quand même » laisse passer jusqu'à la fin de l'invite (utile
pour un `ssh` légitime dans une session de l'admin).

Limite : seules les sessions servies par un peer (`tmuxatlas agent`) sont
surveillées. Les sessions locales d'un hub `standalone` ne le sont pas encore
(`pkg/ws/pty_terminal.go`) ; dans la topologie visée, les sessions des agents
sont toujours celles du peer `agents`.

## Modèle de menace

| Menace | Parade |
|---|---|
| Agent qui lance `sudo` lui-même | `agents` n'a aucun droit sudo. Le hook n'est qu'un rappel. |
| Agent qui lit le mot de passe | Il n'est tapé que dans le PTY du peer sudo (autre compte Unix) ; capture par pipes, caviardage, relecture. Le hub et le peer ne journalisent jamais de payload PTY. |
| Agent qui modifie la commande après approbation | Approbation liée à la version ; plus d'édition hors `pending` ; le peer exécute la commande reçue du hub pour cette version. |
| Agent qui se fait passer pour le peer sudo | Épinglage `TMUXATLAS_SUDO_PEERS` par empreinte ; refus si le peer sudo est l'hôte d'origine. |
| Agent qui imite une invite sudo dans son pane | Bandeau permanent « Hôte non fiable » sur tout hôte autre que le hub et les peers sudo épinglés. En aide seulement : blocage des frappes quand l'écho est coupé, contournable par un pane en mode brut. |
| Commande trompeuse (blancs, bidi, caractères invisibles) | Le hub refuse les catégories Unicode Cc, Cf, Zl, Zp, plus de 2 blancs à la suite et plus de 1000 caractères ; le panneau montre la commande entière avec longueur et sha256 ; le terminal sudo la réimprime avant l'invite. |
| Rejeu d'une approbation | Grant à usage unique, 60 s, lié à l'admin qui l'a obtenu, transmis dans la 1re trame WebSocket (pas dans l'URL ni les journaux). |
| Page tierce qui pilote l'API | `Origin` stricte, CSRF, cookie `SameSite=Strict`. |
| Cookie admin volé | Il faut encore le mot de passe sudo de l'admin, tapé en direct. Voir « Reste à faire ». |
| Sortie qui contient un secret | Relecture obligatoire : Envoyer / Code seul / Rien ; sans relecture, rien ne part. |
| `NOPASSWD` sur le compte admin | Refus d'ouvrir le terminal. |

**Risque résiduel accepté** : le mot de passe transite en clair dans la mémoire
du hub (relais des octets du PTY), qui tourne sous `transcriptflow`, lui-même
membre du groupe sudo. Un attaquant qui contrôle `transcriptflow` a déjà accès à
un compte sudoer : le hub n'élargit pas la surface. Le compte `agents` ne peut ni
lire la mémoire du hub, ni remplacer son binaire, ni modifier sa configuration.
À vérifier sur l'hôte : `kernel.yama.ptrace_scope` ≥ 1.

## Reste à faire

- **Ré-authentification par passkey liée à la commande** (challenge =
  sha256(id ‖ version ‖ commande ‖ peer ‖ nonce), vérifié par le peer sudo contre
  des clés épinglées chez l'admin). Non faite : le mode comptes du fork n'a pas
  de passkeys par compte, et le mode passkey ne sait pas encore produire un
  challenge choisi. À faire après `feat/comptes-roles`.
- Shell interactif après la commande (aujourd'hui : seulement la commande,
  modifiable et relançable).
- Mode `--tty` sans capture (pour `apt` en plein écran…), avec retour réduit au
  code de sortie.
- Choix de l'admin destinataire quand il y en a plusieurs (aujourd'hui : tous
  les admins voient la demande, le premier qui approuve la prend).
- Persistance des demandes au redémarrage du hub (aujourd'hui : en mémoire).

# Terminal sudo

Un agent (Claude Code) n'a pas de TTY et ne peut pas taper de mot de passe. Quand
il a besoin de `sudo`, il **propose** une commande : la demande apparaît dans le
cockpit de sa session ; un administrateur peut la modifier, l'approuver, et
obtient alors un **terminal interactif** sous son propre compte, où la commande
proposée attend à l'invite **sans être lancée**. L'humain la relit, la lance, la
modifie ou tape d'autres commandes, donne lui-même son mot de passe à `sudo`,
puis indique « Terminé » ou « Refusé ».

Deux garanties :

1. **Rien ne s'exécute sans l'humain.** C'est lui qui appuie sur Entrée dans un
   shell qu'il contrôle ; la commande proposée n'est qu'un texte pré-rempli.
2. **L'agent ne voit ni le mot de passe, ni la sortie, ni les frappes.** Il ne
   reçoit que `{id, status, note}` : `done`, `refused`, `expired` ou
   `cancelled`, et la note que l'humain a tapée.

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
                                              ◀─/ws/peer-pty──  PTY : bash --noprofile --norc -i
```

## Cycle de vie (`pkg/sudo`)

```
pending ──edit (v+1)──▶ pending
pending ──approve(version exacte, peer)──▶ approved  (grant à usage unique, 60 s)
approved ──grant non utilisé──▶ pending
approved ──redeem (même admin)──▶ running   (terminal ouvert, commande pré-remplie)
running ──terminal impossible à ouvrir──▶ pending (avec la raison)
running ──fin du shell, inactivité, durée max──▶ closed
running | closed ──Terminé──▶ done
pending | approved | running | closed ──Refusé──▶ refused
Autres fins : cancelled (par l'agent, avant l'ouverture), expired
```

Une décision prise pendant que le terminal est ouvert le ferme.

Durées (défauts, `sudo.DefaultLimits`) :

| Étape | Durée |
|---|---|
| Demande en attente / approuvée | 15 min après création, puis `expired` |
| Grant (ouvrir le terminal) | 60 s, puis retour en `pending` |
| Terminal (côté peer) | 15 min au total, 5 min sans frappe ni sortie |
| Terminal (plafond vu du hub) | 16 min, puis `closed` |
| Décision après fermeture | 30 min, puis `expired` |

La commande proposée tient sur **une seule ligne** d'au plus 1000 caractères,
sans caractère de contrôle (C0, C1, tabulation comprise), de format (bidi,
largeur nulle, BOM), de séparateur de ligne ou de paragraphe, ni plus de deux
blancs à la suite : elle est tapée telle quelle dans readline, aucune touche
spéciale ne peut donc la lancer ou la réécrire. La raison suit les mêmes règles.

## Côté agent

```bash
tmuxatlas sudo-request --reason "installer jq" -- "apt-get install -y jq"
```

- Passe par le socket Unix local (celui du `tmuxatlas agent` du compte `agents`),
  qui transmet au hub par le canal de contrôle déjà authentifié (Ed25519).
- Long-poll : attend 8 min par défaut (`--timeout`), sous la limite de 10 min du
  Bash tool. Au-delà : code de sortie **75** et
  `tmuxatlas sudo-request --wait <id>` pour reprendre.
- Codes de sortie : 0 = `done`, 1 = `refused`, `expired` ou `cancelled`,
  75 = toujours en attente, 2 = erreur d'usage ou de transport.
- Sortie : `{id, status, note}`. Jamais la sortie de la commande : l'agent
  vérifie lui-même l'effet (`which jq`, `systemctl is-active …`).
- Ctrl-C (ou `--cancel <id>`) annule une demande qui n'a pas encore démarré.

Seuls les hôtes listés dans `TMUXATLAS_SUDO_REQUESTERS` (ou
`--sudo-requesters`) peuvent déposer une demande : des **empreintes** séparées
par des virgules. Liste vide = toute demande est refusée (`forbidden`), y
compris celles des agents qui tournent sur l'hôte du hub (il faut alors y mettre
l'empreinte du hub). Chaque hôte a au plus 4 demandes ouvertes et 12 demandes
par tranche de 10 minutes, annulées comprises ; la file entière est plafonnée à
32. Le panneau affiche l'empreinte du demandeur, seule information établie par
le hub.

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
| `GET /api/sudo/requests/{id}` | Détail (avec la durée et la raison de fermeture du terminal) |
| `POST /api/sudo/requests/{id}/edit` | `{version, command}` |
| `POST /api/sudo/requests/{id}/approve` | `{version, peer}` → `{grant, expires_at}` |
| `POST /api/sudo/requests/{id}/done` | `{note}`, terminal ouvert ou fermé |
| `POST /api/sudo/requests/{id}/refuse` | `{note}`, à tout moment avant une décision |
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

1. **Demande** : empreinte du demandeur (établie par le hub), raison, origine
   déclarée, commande entière dans un bloc qui passe à la ligne avec sa
   longueur et son sha256 court, champ d'édition (versionnée, l'original reste
   affiché), choix du peer sudo, Approuver / Refuser.
2. **Terminal** : xterm dédié, sans brouillons, sans `localStorage`, sans
   composeur mobile. Un bandeau signale la saisie masquée.
3. **Issue** : « Terminé » ou « Refusé », avec une note optionnelle. C'est tout
   ce que l'agent recevra.

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

1. PTY éphémère, hors tmux, nouvelle session (`setsid`) ; environnement
   assaini (`PATH` fixe, `HISTFILE=/dev/null`, pas de `SSH_AUTH_SOCK`), shell
   `bash --noprofile --norc -i` sous le compte de l'admin.
2. Avant le shell, le terminal imprime :
   `# Proposé par <empreinte du demandeur> : <commande>`, puis sa longueur et
   les 12 premiers caractères de son sha256, les mêmes que dans le panneau.
3. Dès que readline lit la ligne, la commande est **tapée à l'invite sans
   Entrée** : `sudo <commande>` (ou la commande telle quelle si elle commence
   déjà par `sudo`). Attention : dans `sudo a && b`, seul `a` passe par sudo ;
   c'est à l'humain de relire et d'ajuster.
4. L'humain fait ce qu'il veut dans ce shell ; `sudo` lui demande son mot de
   passe normalement. Pas de `sudo -k` imposé ni de refus du `NOPASSWD` : c'est
   l'humain qui tape.
5. L'état de l'écho du PTY est surveillé : quand il est coupé (invite de mot de
   passe), le navigateur affiche « saisie masquée ».
6. Fin : `exit`, fermeture de l'onglet, décision prise dans le panneau,
   15 min au total ou 5 min sans activité. Le groupe de processus du shell est
   tué, ainsi que tout processus resté dans sa session (tâches de fond).
7. Rien n'est capturé : la sortie va à l'écran de l'admin seulement, les
   frappes au PTY seulement. Le hub n'apprend que la durée et la raison de la
   fermeture.

Audit faisant foi : `$XDG_STATE_HOME/tmuxatlas/sudo-audit.jsonl` (défaut
`~/.local/state/tmuxatlas/`, mode 0600) chez le compte de l'admin. Une ligne par
ouverture, refus ou fermeture : demande, version, commande **proposée** et son
empreinte sha256, demandeur, approbateur, origine, code de sortie du shell,
durée. **Jamais les frappes ni la sortie**, donc pas non plus les commandes
réellement tapées : pour celles passées par sudo, voir `auth.log`. `tmuxatlas sudo-audit` l'affiche. Complément : journald et
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
| Agent qui lit le mot de passe | Il n'est tapé que dans le PTY du peer sudo (autre compte Unix). Rien de ce terminal ne revient à l'agent : ni sortie, ni frappes. Le hub et le peer ne journalisent jamais de payload PTY. |
| Agent qui modifie la commande après approbation | Approbation liée à la version ; plus d'édition hors `pending` ; le peer pré-remplit la commande reçue du hub pour cette version, et rien ne part sans Entrée. |
| Commande pré-remplie qui se lance seule | Aucun caractère de contrôle accepté : rien dans le texte tapé ne peut valoir Entrée ou une touche d'édition. |
| Agent qui se fait passer pour le peer sudo | Épinglage `TMUXATLAS_SUDO_PEERS` par empreinte ; refus si le peer sudo est l'hôte d'origine. |
| Agent qui imite une invite sudo dans son pane | Bandeau permanent « Hôte non fiable » sur tout hôte autre que le hub et les peers sudo épinglés. En aide seulement : blocage des frappes quand l'écho est coupé, contournable par un pane en mode brut. |
| Commande trompeuse (blancs, bidi, caractères invisibles) | Le hub refuse les catégories Unicode Cc, Cf, Zl, Zp, plus de 2 blancs à la suite et plus de 1000 caractères ; le panneau montre la commande entière avec longueur et sha256 ; le terminal sudo la réimprime avant l'invite. |
| Rejeu d'une approbation | Grant à usage unique, 60 s, lié à l'admin qui l'a obtenu, transmis dans la 1re trame WebSocket (pas dans l'URL ni les journaux). |
| Demandes en rafale ou d'origine maquillée | Seuls les hôtes de `TMUXATLAS_SUDO_REQUESTERS` déposent ; 4 ouvertes et 12 par 10 min par hôte ; l'empreinte du demandeur est affichée. |
| Page tierce qui pilote l'API | `Origin` stricte, CSRF, cookie `SameSite=Strict`. |
| Cookie admin volé | Il ouvre un shell sous le compte de l'admin, limité à 15 min ; `sudo` y demande encore le mot de passe, sauf `NOPASSWD`, désormais accepté. Voir « Reste à faire ». |
| Sortie qui contient un secret | La sortie ne quitte jamais le navigateur de l'admin. |
| `NOPASSWD` sur le compte admin | Accepté : c'est l'humain qui tape les commandes. Le shell reste sous son compte, limité en durée. |

**Risque résiduel accepté** : le mot de passe transite en clair dans la mémoire
du hub (relais des octets du PTY), qui tourne sous `transcriptflow`, lui-même
membre du groupe sudo. Un attaquant qui contrôle `transcriptflow` a déjà accès à
un compte sudoer : le hub n'élargit pas la surface. Le compte `agents` ne peut ni
lire la mémoire du hub, ni remplacer son binaire, ni modifier sa configuration.
À vérifier sur l'hôte : `kernel.yama.ptrace_scope` ≥ 1.

## Reste à faire

- **Ré-authentification par passkey liée à la demande** avant d'ouvrir le
  terminal. Non faite : le mode comptes du fork n'a pas de passkeys par compte.

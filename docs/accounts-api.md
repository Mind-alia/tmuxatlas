# API du mode comptes

Le mode `accounts` est sélectionné avec `TMUXATLAS_AUTH=accounts` ou
`--auth accounts`. L’amorçage et les limites d’isolation Unix sont décrits
dans le [README](../README.md#comptes-atlas). Les identifiants de propriétaire
sont les champs `id` stables des comptes ; une session libre a `owner: null`.

## Authentification

Les mutations exigent l’en-tête `Origin` correspondant à `TMUXATLAS_PUBLIC_URL`.
Après connexion, elles exigent également `X-TmuxAtlas-CSRF`, dont la valeur est
fournie par la connexion et le statut. Les corps indiqués ci-dessous sont JSON
et utilisent `Content-Type: application/json`. Le cookie de session est
`HttpOnly`, `SameSite=Strict`, `Path=/` et `Secure` si la requête est en HTTPS
(les en-têtes de proxy ne sont utilisés que depuis la boucle locale).

| Route | Corps ou réponse |
|-------|------------------|
| `POST /api/auth/login` | `{"username":"alice","password":"…"}` ; réponse `{"authenticated":true,"username":"alice","role":"guest","csrf_token":"…"}` et cookie. Échec : 401 générique ; limitation : 429. |
| `GET /api/auth/status` | `{"mode":"accounts","authenticated":false,"username":"","role":""}` ; une session active contient aussi `csrf_token`. |
| `POST /api/auth/logout` | Révoque la session courante et renvoie 204. |
| `POST /api/auth/change-password` | `{"old_password":"…","new_password":"…"}` ; 204, les autres sessions du compte sont révoquées. |
| `POST /api/auth/invitation/inspect` | Public. `{"token":"…"}` ; `{"username":"alice","expires_at":"…","has_password":false}`, ou 404 si le lien est invalide, utilisé ou expiré. |
| `POST /api/auth/invitation/accept` | Public. `{"token":"…","password":"…"}` ; 204. Le mot de passe est enregistré, le lien consommé et les sessions du compte révoquées. |

Les deux routes d’invitation exigent `Origin` et partagent la limitation des
connexions. Le jeton n’apparaît jamais dans une URL envoyée au serveur : le
lien a la forme `/invite#<jeton>` et la page le lit dans le fragment.

Les noms respectent `^[a-zA-Z0-9._-]{2,32}$` et sont uniques sans distinction
de casse. Les mots de passe contiennent de 10 à 200 caractères. Les réponses
ne contiennent ni mot de passe ni hash.

## Administration

Ces routes exigent le rôle `admin`. Il y a deux rôles : `admin`, qui voit tous
les hôtes, et `guest` (invité), qui ne voit que ses hôtes. Plusieurs
administrateurs sont possibles. Un ancien rôle `user`, ou tout rôle inconnu, est
lu comme `guest`. Un objet compte a la forme
`{"id":"…","username":"alice","role":"guest","disabled":false,"created_at":"…","password_changed_at":"…","hosts":["<host_id>"],"unix_user":"inv-alice","pending":false}`
(`invitation_expires_at` s’ajoute quand un lien est en attente).

| Route | Corps ou réponse |
|-------|------------------|
| `GET /api/admin/users` | `{"users":[…]}`. |
| `POST /api/admin/users` | `{"username":"alice","password":"…","role":"guest","unix_user":"inv-alice"}` ; 201 et objet compte. Sans `password`, le compte attend son invitation et la réponse contient `invitation: {"token","path":"/invite#…","expires_at"}`. |
| `PATCH /api/admin/users/{id}` | Champs facultatifs, au moins un : `password`, `disabled`, `role`, `hosts` (liste complète des `host_id`), `unix_user`. Réponse : objet compte. 409 pour un hôte déjà attribué à un autre invité ou pour le dernier administrateur ; 403 pour l’hôte du hub. |
| `POST /api/admin/users/{id}/invitation` | `{"hours":24}` facultatif (7 jours au plus) ; 201 et `{"token","path","expires_at"}`. Remplace le lien précédent ; sert aussi à réinitialiser un mot de passe. |
| `DELETE /api/admin/users/{id}/invitation` | Annule le lien ; 204. |
| `POST /api/admin/users/{id}/pairing` | `{"code","expires_at"}` : code d’appairage de 5 minutes. L’hôte appairé avec ce code est attribué à ce compte ; si l’attribution échoue, l’appairage est annulé. |
| `GET /api/admin/hosts` | `{"hosts":[{"id","name","online","local","grantable","guest"}]}` : hôtes connus, invité qui détient chacun. |
| `DELETE /api/admin/users/{id}` | Libère les sessions connues du compte et le supprime ; 204. La suppression échoue si un hôte connu est hors ligne. |
| `GET /api/admin/sessions` | Tableau des vues complètes des sessions et de leur `owner`. |

Un administrateur ne peut pas se désactiver, se supprimer ou changer son propre
rôle, ni désactiver, supprimer ou rétrograder le dernier administrateur actif
(un administrateur qui n’a pas encore accepté son invitation ne compte pas).
Retirer un hôte ou rétrograder un administrateur révoque les sessions du compte
et ferme ses WebSockets. Désactivation, réinitialisation du
mot de passe et suppression révoquent les sessions du compte.

## Sessions et autorisations utilisateur

Les actions utilisent `host_id` pour identifier explicitement l’hôte. Les
réponses d’action réussies ont la forme `{"request_id":"…","result":{…}}`.

| Route | Paramètres |
|-------|------------|
| `POST /api/session/claim` | `{"host_id":"…","session":"travail"}` ; une session déjà attribuée renvoie 409 avec `code: "already-owned"`. Un administrateur peut ajouter `"owner":"<id>"` pour réattribuer. |
| `POST /api/session/release` | `{"host_id":"…","session":"travail"}` ; l’administrateur peut libérer toute session. |
| `POST /api/session/new` | `{"host_id":"…","session":"travail","cwd":"…","agent":"…"}` ; `cwd` et `agent` sont facultatifs. Le créateur devient propriétaire. |
| `POST /api/session/rename` | `{"host_id":"…","session":"travail","new_name":"nouveau"}`. |
| `POST /api/session/kill` | `{"host_id":"…","session":"travail"}`. |
| `POST /api/session/select-window` | `{"host_id":"…","session":"travail","window":0,"pane":"%1"}` ; `pane` est facultatif. |
| `POST /api/session/select-pane` | `{"host_id":"…","session":"travail","pane":"%1"}`. |
| `POST /api/session/reply` | `{"host_id":"…","session":"travail","pane":"%1","key":"1","fingerprint":"…"}`. |
| `GET /api/session/preview` | Paramètres `host_id`, `session`, et facultativement `pane`, `lines`. |
| `GET /api/session/directories` | Paramètre `host_id`. |
| `DELETE /api/tool-event` | `{"host":"…","session":"travail","window":0,"pane":"%1"}`. |
| `GET /ws/session` | Paramètres `host`, `name`, et facultativement `cols`, `rows` ; terminal PTY. |
| `GET /ws/events` | Paramètre `schema=1` ; instantanés filtrés par compte. |

Un invité peut lire `/api/sessions`, `/api/hosts`, `/api/tool-events`,
`/api/activity`, `/api/preferences` (valeurs par défaut) et son statut, se
déconnecter et changer son mot de passe. Toutes les données sont filtrées sur
ses hôtes, y compris les instantanés de `/ws/events`, rechargés à chaque envoi.
Les routes qui nomment un hôte (`host_id`, `host`) ou une session vérifient
l’attribution côté serveur. Sur un hôte attribué, le propriétaire d’une session
reste une étiquette ; un invité ne peut prendre une session que pour lui-même.
Son annuaire `/api/accounts/directory` ne contient que lui. Les autres routes
protégées lui sont refusées par défaut : administration, surveillance
(`/api/monitor`), statistiques (`/api/stats`), voix (`/api/voice`, `/ws/mic`),
peers, push et écriture des préférences.

L’hôte du hub (son propre tmux) n’est jamais attribué à un invité, car le hub
tourne sous le compte de production. `TMUXATLAS_GUEST_LOCAL_HOST=allow` lève
cette règle pour un hub qui tourne sous un compte dédié. Un hôte ne peut être
attribué qu’à un seul invité.

## Ligne de commande

`tmuxatlas users` agit directement sur `users.json` et se lance donc sous le
compte Unix du hub : `list`, `add --username alice [--role guest|admin]
[--unix-user inv-alice] [--valid-for 24h]` (affiche le lien d’invitation),
`invite --username alice`, `set-role --username alice --role admin` et
`bootstrap-admin`. Un WebSocket déjà ouvert garde l’ancien rôle jusqu’à sa
reconnexion : redémarrer le hub pour couper immédiatement.

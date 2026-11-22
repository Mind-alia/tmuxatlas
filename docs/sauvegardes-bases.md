# Sauvegardes des bases de données des conteneurs

Atlas sait sauvegarder lui-même les bases PostgreSQL, MySQL et MariaDB qui
tournent dans des conteneurs Docker du serveur : à la demande ou selon une
planification, avec une rétention, une alerte quand une sauvegarde planifiée
échoue, le téléchargement des fichiers et, si on l’active, la restauration.

L’interface est dans **Conteneurs → Sauvegardes et alertes**, section « Bases
de données des conteneurs ». Elle est réservée aux administrateurs.

Cette fonction est distincte du panneau « Sauvegardes » décrit dans
[`ops.md`](ops.md), qui ne fait que *surveiller* une sauvegarde externe (script
+ copie sur R2) à partir des fichiers d’état qu’elle publie. Les deux se
complètent : voir « Limites » plus bas.

## Fonctionnement

### Détection

À chaque affichage, Atlas liste les conteneurs **en cours d’exécution** et
reconnaît une base :

- d’après le nom de l’image : `postgres`, `postgresql`, `postgis`,
  `timescaledb`, `pgvector` (PostgreSQL) ; `mysql`, `mysql-server`, `percona`
  (MySQL) ; `mariadb` (MariaDB). Le nom doit correspondre exactement :
  `postgres-exporter` ou `pgadmin4` ne sont pas des bases ;
- sinon d’après les **noms** des variables que les images officielles
  définissent elles-mêmes (`PGDATA` et `PG_MAJOR`, `MARIADB_VERSION`,
  `MYSQL_MAJOR`…), ce qui couvre les images reconstruites à partir d’elles.
  Une application qui possède seulement `POSTGRES_PASSWORD` ou `MYSQL_HOST`
  pour se connecter à une base n’est pas prise pour une base.

Les valeurs des variables d’environnement ne sont jamais renvoyées au
navigateur, ni écrites dans les journaux ou dans les messages d’erreur.

### Sauvegarde

Le dump est exécuté **dans le conteneur** par l’API `exec` de Docker, sans
shell : la commande est un tableau d’arguments.

| Moteur | Commande | Compte |
|--------|----------|--------|
| PostgreSQL | `pg_dumpall --clean --if-exists` | `POSTGRES_USER` du conteneur, sinon `postgres` |
| MySQL | `mysqldump --all-databases --single-transaction --routines --events` (à défaut `mariadb-dump`) | `root`, sinon le compte applicatif |
| MariaDB | `mariadb-dump --all-databases --single-transaction --routines --events` (à défaut `mysqldump`) | `root`, sinon le compte applicatif |

Le mot de passe est lu dans l’environnement du conteneur (`POSTGRES_PASSWORD`,
`MYSQL_ROOT_PASSWORD`, `MARIADB_ROOT_PASSWORD`…) et transmis par
l’environnement du seul processus de dump (`PGPASSWORD`, `MYSQL_PWD`) : il
n’apparaît jamais sur la ligne de commande. Un mot de passe fourni au conteneur
par un fichier (`*_PASSWORD_FILE`, secrets Docker) n’est pas lu ; PostgreSQL
s’en passe pour les connexions locales de l’image officielle, MySQL et MariaDB
non.

La sortie est compressée en gzip au fil de l’eau, écrite dans un fichier
temporaire puis renommée une fois complète. Atlas enregistre la taille, la
durée et la somme SHA-256 du fichier compressé.

Une sauvegarde est **ratée** — et son fichier supprimé — quand le programme
sort avec une erreur, ne produit rien, dépasse une heure, ou quand l’écriture
échoue. Elle reste dans la liste, avec la raison, sans fichier. Les cinq
derniers échecs sont gardés.

Avant de commencer, Atlas vérifie l’espace libre : il refuse s’il reste moins
de 1 Gio plus la taille de la dernière sauvegarde réussie de cette base.

Une même base n’a jamais deux sauvegardes en même temps, et les sauvegardes
planifiées de bases différentes passent l’une après l’autre.

### Où sont les fichiers

```
<données d’Atlas>/db-backups/
  config.json                         planifications, rétentions, dernier échec
  <conteneur>/<AAAAMMJJ>T<HHMMSS>Z.sql.gz   le dump compressé (heure UTC)
  <conteneur>/<AAAAMMJJ>T<HHMMSS>Z.json     origine, état, taille, durée, SHA-256
```

`<données d’Atlas>` est `$XDG_DATA_HOME/tmuxatlas`, par défaut
`~/.local/share/tmuxatlas`, du compte qui fait tourner le hub. Les dossiers
sont en `0700`, les fichiers en `0600`. Le chemin exact est rappelé en haut de
la section dans l’interface.

Pour vérifier ou relire un fichier à la main :

```sh
sha256sum 20261122T021500Z.sql.gz     # à comparer au champ sha256 du .json
gunzip -c 20261122T021500Z.sql.gz | less
```

### Planification et rétention

Par base : désactivée, tous les jours à HH:MM, ou toutes les semaines un jour
donné à HH:MM, à l’**heure locale du serveur**. Le planificateur est interne au
hub (pas de cron) et regarde toutes les 30 secondes ce qui est dû.

- Si le hub était arrêté à l’heure prévue, il rattrape **une** sauvegarde au
  démarrage, la dernière due, quel que soit le nombre d’occurrences manquées.
- Une planification qu’on vient de régler ne déclenche rien pour une heure déjà
  passée.
- Tant que le hub est arrêté, rien n’est sauvegardé : la planification vit
  dans le hub.

La rétention garde les N dernières sauvegardes réussies (7 par défaut, de 1 à
365), toutes origines confondues. Elle n’est appliquée **qu’après une
sauvegarde réussie**, manuelle ou planifiée : une base dont les sauvegardes
échouent garde ses anciennes copies. Les sauvegardes de sécurité faites avant
une restauration ne suppriment rien au moment où elles sont faites ; elles
comptent ensuite comme les autres.

### Alertes

Une sauvegarde **planifiée** ratée lève une alerte critique « Sauvegarde de la
base … en échec » dans le système de notifications existant (notifications du
navigateur et ntfy, voir [`ops.md`](ops.md)), avec ses rappels et son
anti-spam. Elle dure jusqu’à la prochaine sauvegarde réussie de cette base,
manuelle ou planifiée : la notification de rétablissement la remplace alors.
Désactiver la planification la retire aussi. Une sauvegarde manuelle ratée ne
notifie pas : son résultat est sous les yeux de la personne qui l’a lancée.

Il faut qu’au moins un canal soit configuré, et `TMUXATLAS_OPS_NOTIFY` ne doit
pas valoir `off`.

### Téléchargement et suppression

Chaque sauvegarde réussie se télécharge (`<conteneur>-<date>.sql.gz`) et se
supprime, après confirmation. Les fichiers sont désignés par le nom du
conteneur et l’identifiant de la sauvegarde, tous deux validés ; aucun chemin
n’est jamais accepté du navigateur. Les sauvegardes d’un conteneur arrêté ou
supprimé restent listées, téléchargeables et supprimables.

## Restauration

**La restauration remplace le contenu de la base. Elle est désactivée par
défaut.**

### Activation

Démarrer le hub avec :

```
TMUXATLAS_DB_RESTORE=on
```

Sans cette variable, le bouton « Restaurer » est grisé et le serveur refuse la
requête.

### Déroulement

1. Dans l’interface, il faut retaper le nom exact du conteneur ; le serveur le
   revérifie.
2. Le serveur recalcule la somme SHA-256 du fichier et refuse si elle diffère de
   celle enregistrée à la sauvegarde.
3. Il vérifie que le conteneur tourne et que son moteur est celui de la
   sauvegarde.
4. Il fait une **sauvegarde de sécurité** de l’état actuel (origine « avant
   restauration »). Si elle échoue, rien n’est restauré.
5. Il envoie le dump décompressé sur l’entrée standard de `psql` (base
   `postgres`), `mysql` ou `mariadb`, dans le conteneur d’origine.

Une seule restauration à la fois sur le serveur ; pendant ce temps, aucune
sauvegarde ni suppression sur cette base. Le résultat, le code de sortie et le
début de la sortie d’erreur du client (16 Kio au plus) s’affichent en haut de
la section. Ce résultat n’est gardé qu’en mémoire : il disparaît au
redémarrage du hub, les journaux du hub en gardent la trace.

### Limites, à lire avant de restaurer

- **Ce n’est pas une restauration à un instant donné.** On revient à l’état du
  moment de la sauvegarde ; tout ce qui a été écrit depuis est perdu (sauf dans
  la sauvegarde de sécurité).
- **Arrêter d’abord les applications qui utilisent la base.** Le dump
  PostgreSQL commence par supprimer puis recréer chaque base ; une base à
  laquelle une application est encore connectée ne peut pas être supprimée, et
  la suite produit alors des erreurs d’objets déjà existants et de données en
  double. La base peut se retrouver dans un état mélangé.
- **`psql` continue après une erreur.** Une restauration PostgreSQL peut donc
  se terminer avec le code 0 tout en ayant affiché des erreurs — certaines sont
  bénignes (le rôle courant ne peut pas être supprimé, « role already
  exists »), d’autres non. L’interface le signale par « terminée, avec des
  messages du client » : **il faut lire la sortie**.
- Une restauration interrompue (délai de deux heures, hub redémarré, conteneur
  arrêté) laisse la base dans un état partiel. La sauvegarde de sécurité permet
  de revenir à l’état d’avant.
- `pg_dumpall` sauvegarde aussi les rôles et leurs mots de passe ; les
  restaurer remet les mots de passe de l’époque de la sauvegarde.
- Un dump MySQL ou MariaDB `--all-databases` contient la base système `mysql`
  (comptes et droits). Après restauration, les droits ne sont relus qu’après un
  `FLUSH PRIVILEGES` ou un redémarrage du conteneur.
- La restauration se fait dans un conteneur **du même nom**. Restaurer vers
  une version majeure différente du moteur n’est pas garanti.
- Les très grosses bases : le dump et la restauration sont bornés à une et
  deux heures.

Pour restaurer ailleurs, ou autrement, télécharger le fichier et le rejouer à
la main :

```sh
gunzip -c shop-db-1-20261122T021500Z.sql.gz | docker exec -i <conteneur> psql -U <utilisateur> -d postgres
```

## Sécurité

- Routes `/api/db-backups/*` réservées aux administrateurs ; les modifications
  exigent l’en-tête `Origin` du site et le jeton CSRF, comme les actions
  Docker.
- Tout l’accès à Docker passe par la vue Conteneurs : `TMUXATLAS_DOCKER=off`
  ferme aussi les sauvegardes, de même que la règle qui désactive la vue Docker
  quand un compte non administrateur est actif (voir
  [`accounts-security.md`](accounts-security.md) et [`ops.md`](ops.md)). Dans
  ces cas, les sauvegardes planifiées échouent et le signalent.
- Chaque sauvegarde demandée, téléchargement, suppression, changement de
  réglage et restauration est journalisé avec le compte qui l’a fait.
- Les fichiers de sauvegarde contiennent **toutes les données** des bases, en
  clair (compressées, pas chiffrées). Toute personne qui a un terminal sur le
  compte Unix du hub peut les lire, comme elle peut déjà interroger Docker.

## Limites générales

- **Une sauvegarde sur le même disque que la base ne protège pas d’une panne de
  ce disque, ni de la perte du serveur.** Ces fichiers protègent d’une erreur
  de manipulation, d’une migration ratée, d’une suppression accidentelle. Pour
  le reste, il faut une copie ailleurs : télécharger les fichiers, ou garder la
  sauvegarde externe vers R2 que surveille le panneau « Sauvegardes ».
- Seuls les conteneurs en cours d’exécution sont sauvegardés.
- Un seul dump par conteneur, de toutes ses bases ; pas de sauvegarde base par
  base, ni incrémentale.
- Les sauvegardes sont rattachées au **nom** du conteneur : le renommer en
  commence une nouvelle série (l’ancienne reste listée).
- Un dump en cours quand le hub s’arrête est abandonné et marqué raté. Si le
  hub est tué sans pouvoir le noter, le fichier temporaire est supprimé au
  démarrage suivant, sans ligne « ratée » ; une sauvegarde planifiée perdue
  ainsi n’est pas relancée avant sa prochaine occurrence.

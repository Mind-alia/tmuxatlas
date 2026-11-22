# Secrets

La page **Secrets** (`/secrets`) est un coffre pour les variables d'environnement des applications : au lieu de traîner en clair dans des fichiers `.env`, les valeurs sont stockées chiffrées, et ne sont écrites en clair qu'au moment où on le demande. Elle est réservée aux administrateurs.

## Fonctionnement

- Une **application** est un nom (`[a-z0-9][a-z0-9_-]{0,62}`) et une liste de variables `NOM=valeur`. Les noms de variables suivent la règle des shells (lettres, chiffres, `_`, pas de chiffre en premier).
- Chaque **valeur** est chiffrée séparément avec [age](https://age-encryption.org) pour la clé publique du coffre. Les **noms** restent lisibles.
- Comme le chiffrement n'utilise que la clé publique, **ajouter, remplacer ou importer des valeurs ne demande pas de déverrouiller**.
- **Révéler** une valeur, **exporter** une application en `.env` ou l'**appliquer** demande la clé privée, donc le coffre déverrouillé.

### Clé maîtresse

À la création du coffre, l'administrateur choisit une phrase de passe (12 caractères au moins). Le hub génère une identité age X25519 :

- la **clé publique** est enregistrée en clair ;
- la **clé privée** est enregistrée chiffrée par la phrase de passe (age en mode phrase de passe, dérivation scrypt) ;
- la phrase de passe n'est enregistrée nulle part.

Au repos, le hub ne peut donc pas lire les valeurs. **Déverrouiller** = fournir la phrase de passe : la clé privée déchiffrée reste en mémoire 10 minutes, sans prolongation quand on s'en sert, puis est oubliée. **Verrouiller** l'oublie tout de suite. Un redémarrage du hub verrouille aussi.

Après 5 phrases de passe refusées de suite, chaque nouvel échec bloque les essais : 30 secondes, puis le double à chaque fois, jusqu'à 15 minutes. Ce compteur est en mémoire : il repart de zéro au redémarrage du hub.

### Où vivent les fichiers

Dans le dossier de données du hub, `~/.local/share/tmuxatlas/secrets/` sous Linux (dossier en 0700, fichiers en 0600) :

| Fichier | Contenu |
|---|---|
| `vault.json` | clé publique en clair, clé privée chiffrée par la phrase de passe |
| `apps/<nom>.json` | une application : noms en clair, valeurs chiffrées, cible |
| `audit.log` | le journal d'audit |
| `.lock` | verrou des écritures |

Les écritures passent par un fichier temporaire puis un renommage, sous verrou : deux modifications simultanées ne se perdent pas.

**Sauvegarde** : ce dossier se copie tel quel. Une copie sans la phrase de passe ne sert à rien à celui qui la vole — ni à vous.

## Importer un `.env`

Collez le texte dans « Importer un fichier .env ». Il est analysé sur le serveur et chaque valeur est chiffrée aussitôt ; une ligne invalide annule tout l'import. Sont compris : lignes vides, commentaires `#`, préfixe `export `, valeurs nues (un ` #` en fin de ligne est un commentaire), entre apostrophes (texte littéral), entre guillemets (échappements `\n`, `\r`, `\t`, `\\`, `\"`, `\$`). Une valeur entre apostrophes ou guillemets peut tenir sur plusieurs lignes. Un nom répété garde sa dernière valeur. Les messages d'erreur donnent le numéro de la ligne, jamais son contenu.

## Appliquer

Une application peut avoir une **cible** : le chemin d'un fichier `.env` et, si on veut, le nom d'un conteneur à redémarrer. « Appliquer » (coffre déverrouillé, confirmation à l'écran) :

1. écrit le fichier, **en clair**, en 0600, par fichier temporaire puis renommage ;
2. garde le fichier précédent dans `<fichier>.bak` (0600 ; la sauvegarde d'avant est écrasée) ;
3. redémarre le conteneur, s'il y en a un, par le même chemin que le bouton « Redémarrer » de la page Conteneurs — donc avec ses réglages (`TMUXATLAS_DOCKER`, refus tant qu'un compte non administrateur est actif, voir [ops.md](ops.md)).

Si le redémarrage échoue, le fichier est quand même écrit, et la page le dit.

Le chemin cible est vérifié à l'enregistrement **et de nouveau à chaque application** :

- absolu et normalisé (pas de `..`, de `.` ni de barre finale) ;
- son dossier existe et, liens symboliques suivis, se trouve dans le dossier personnel du compte du hub ;
- pas dans le dossier du coffre ni dans les dossiers de configuration et de données d'Atlas ;
- le fichier n'est pas un lien symbolique (même vers un fichier du dossier personnel) ni un dossier ;
- il s'appelle `.env`, `.env.<suffixe>` ou `<nom>.env` : « Appliquer » remplace tout le fichier, on l'empêche donc de viser autre chose qu'un fichier d'environnement.

Dans le fichier écrit, une valeur faite de caractères sans risque est écrite nue, sinon entre apostrophes, sinon entre guillemets avec échappements. Docker Compose et les bibliothèques dotenv relisent ce format ; `docker run --env-file`, lui, ne comprend pas les guillemets.

**Un redémarrage ne suffit pas toujours.** Docker fixe les variables d'un conteneur à sa création. Un conteneur créé par Compose avec `env_file:` ne voit le nouveau fichier qu'après `docker compose up -d` (recréation), pas après un redémarrage. Le redémarrage suffit quand l'application lit elle-même le fichier, monté dans le conteneur.

## Journal d'audit

`audit.log` reçoit une ligne JSON par action, en ajout seul : date, compte (ou `passkey` sans comptes), action, application, nom de variable, et selon le cas la cible ou un nombre de variables. **Jamais de valeur ni de phrase de passe.** Les déverrouillages refusés y figurent. Une révélation, un export ou une application n'a lieu que si sa ligne a pu être écrite. La page affiche les 50 dernières lignes. Le fichier n'est pas tourné automatiquement.

## Activation

| `TMUXATLAS_SECRETS` | Effet |
|---|---|
| absent | coffre actif si le hub a une authentification ; refusé sans authentification |
| `on` | actif même sans authentification |
| `off` | désactivé |

## API

Toutes les routes sont sous `/api/secrets/`, réservées aux administrateurs en mode comptes, et répondent avec `Cache-Control: no-store`. Tout ce qui modifie quelque chose **ou renvoie une valeur** est un `POST`, `PUT` ou `DELETE` soumis aux contrôles d'origine et de jeton CSRF des autres modifications.

| Route | Rôle |
|---|---|
| `GET status` | initialisé ? déverrouillé ? jusqu'à quand ? |
| `POST init`, `POST unlock` (`{"passphrase"}`), `POST lock` | clé maîtresse |
| `GET apps` | applications et noms de variables |
| `POST apps` (`{"name"}`), `DELETE apps/{app}` | créer, supprimer |
| `PUT apps/{app}/vars/{name}` (`{"value"}`), `DELETE apps/{app}/vars/{name}` | définir, supprimer |
| `POST apps/{app}/vars/{name}/reveal` | une valeur en clair (déverrouillé) |
| `POST apps/{app}/import` (`{"text"}`) | import dotenv |
| `POST apps/{app}/export` | l'application en dotenv (déverrouillé) |
| `PUT apps/{app}/target` (`{"env_file","container"}`) | cible ; deux chaînes vides la retirent |
| `POST apps/{app}/apply` | écrire la cible, redémarrer (déverrouillé) |
| `GET audit?limit=N` | dernières lignes du journal |

Un coffre verrouillé répond `423`, une mauvaise phrase de passe `403` (code `wrong_passphrase`), trop d'essais `429` avec `Retry-After`.

## Limites, dites honnêtement

- **Le coffre protège les secrets au repos, pas une application qui tourne.** Une fois appliqué, le fichier cible est un `.env` en clair comme avant, et les variables sont visibles dans le conteneur (`docker inspect`, `/proc`). Le gain : une seule copie maîtresse chiffrée, des sauvegardes du coffre qui ne fuient rien, plus de secrets collés dans des notes ou des dépôts, et une trace de qui a lu quoi.
- **Phrase de passe perdue = secrets perdus.** Il n'y a ni récupération ni porte dérobée. Il n'y a pas non plus, pour l'instant, de changement de phrase de passe dans l'interface.
- **Tant que le coffre est déverrouillé**, la clé privée est dans la mémoire du hub : quiconque contrôle le compte Unix du hub à ce moment-là peut la lire. Les valeurs révélées passent par le navigateur de l'administrateur.
- **Tous les comptes Atlas partagent le compte Unix du hub** (voir [accounts-security.md](accounts-security.md)). Un compte non administrateur ne peut pas utiliser l'API du coffre, mais depuis son terminal il peut lire les fichiers du coffre (chiffrés), **lire les fichiers `.env` appliqués (en clair)**, et modifier ou supprimer les fichiers du coffre. Le coffre ne crée pas de séparation entre comptes.
- **Le chiffrement ne signe rien.** Quelqu'un qui peut écrire dans le dossier du coffre peut y remplacer une valeur chiffrée par une autre (la clé publique suffit pour chiffrer) ou supprimer des lignes du journal. Le journal est une trace, pas une preuve.
- **Le hub voit la phrase de passe et les valeurs** au moment où elles lui sont envoyées (déverrouillage, ajout, import) : il faut un hub de confiance et une connexion HTTPS.
- **Ce n'est pas le format SOPS.** Ce sont des fichiers JSON dont chaque valeur est un fichier age « armored » ordinaire.

## Lire le coffre sans Atlas

Les valeurs se déchiffrent avec l'outil `age` standard et `jq`, sans le hub :

```sh
cd ~/.local/share/tmuxatlas/secrets

# 1. La clé privée : age demande la phrase de passe du coffre.
#    /dev/shm est en mémoire : la clé en clair ne touche pas le disque.
jq -r .identity vault.json > /dev/shm/identity.age
age -d -o /dev/shm/key.txt /dev/shm/identity.age

# 2. Une valeur.
jq -r '.vars[] | select(.name == "API_TOKEN") | .value' apps/mon-app.json > /dev/shm/value.age
age -d -i /dev/shm/key.txt /dev/shm/value.age

# 3. Effacer la clé en clair.
rm -f /dev/shm/key.txt /dev/shm/identity.age /dev/shm/value.age
```

`jq -r .public_key vault.json` donne la clé publique (`age1…`) : `age -a -r <clé publique>` produit une valeur que le coffre saura lire.

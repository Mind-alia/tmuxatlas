# Limites de sécurité des comptes

Un terminal donne les droits du compte Unix de son hôte. Les filtres de
l’interface et de l’API ne constituent pas une isolation Unix : une personne
disposant d’un terminal peut lire les fichiers accessibles à ce compte et
modifier les options tmux, notamment `@atlas-owner`.

## Un compte Linux par personne invitée

L’isolation réelle vient du système :

- chaque invité a son propre compte Linux (`useradd`, `chmod 700` sur son
  dossier, ni groupe `docker` ni `sudo`), avec son propre `tmuxatlas agent` ;
- l’administrateur lui attribue seulement l’hôte de ce peer (son empreinte de
  clé, jamais son nom) : le plus simple est un code d’appairage créé pour son
  compte depuis `/admin`, qui attribue l’hôte automatiquement ;
- un hôte n’est attribué qu’à un seul invité, et l’hôte du hub jamais.

La création du compte Linux demande root : elle reste une étape humaine.
L’interface affiche les commandes à copier, mais n’exécute rien.

## Ce qu’un administrateur peut faire

Un administrateur TmuxAtlas voit tous les hôtes et ouvre des terminaux sur
chacun, y compris celui du hub, qui tourne sous le compte de production. Être
administrateur dans TmuxAtlas donne donc un shell sous ces comptes, même si son
propre compte Linux n’a ni `sudo` ni `docker`. Ne donnez ce rôle qu’à qui peut
déjà les utiliser.

## Ce que le hub ne transmet plus

Le hub n’envoie plus aux peers l’état des autres hôtes : un peer d’invité ne
reçoit ni les sessions de la production ni celles des autres invités. Il reste
un risque de fond : le hub peut piloter tous les peers. Un compromis du compte
du hub compromet aussi les comptes de tous les peers.

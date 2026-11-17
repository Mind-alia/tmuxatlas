# Boucles

Une mission poursuit un objectif avec un périmètre, des critères de réussite et une limite de passes ou de temps. L’entretien continu peut s’arrêter lorsque l’agent signale `LOOP_DONE` : son bilan indique alors une attente de nouveau travail. Reprendre ou ajouter une consigne reste une action explicite.

Les nouvelles missions proposent un worktree Git dédié. Les anciennes boucles conservent leurs paramètres et ne sont pas relancées automatiquement.

## Résultats et vérifications

Les agents sont invités à produire une ligne `ATLAS_RESULT` suivie d’un objet JSON contenant les chaînes `problem`, `change`, `checks`, `limitations`, `next` et `decision`. Une sortie non conforme reste consultable dans les logs et n’est pas transformée en succès.

La mémoire persistante conserve les bilans structurés et les consignes appliquées, dans une fenêtre limitée à 24 Ko. Elle est transmise aux passes suivantes, y compris après changement de CLI. Le plan structuré est éditable pendant une pause. Les étapes terminées déclarées par l’agent sont conservées lorsque le contrôle configuré ne signale pas d’échec.

Une commande de vérification facultative est exécutée dans le dossier de travail après la passe. Elle dispose d’un délai maximal de cinq minutes. Son résultat observé est distinct des vérifications déclarées par l’agent. Une fin déclarée par l’agent demande une revue ; le bouton de validation enregistre une validation humaine. Atteindre une limite de passes ou de temps ne valide pas l’objectif.

La limite de temps est mesurée depuis le premier démarrage, pauses comprises. Le contrôle interrompt la passe à l’échéance et borne les attentes de disponibilité.

## Historique Git

Chaque nouvelle passe enregistre un diff entre deux instantanés Git, avec les fichiers suivis, indexés et non suivis non ignorés par Git. L’index utilisateur n’est pas modifié. Les diffs sont limités à 128 Ko et signalés lorsqu’ils sont tronqués. Les sous-modules et fichiers ignorés ne sont pas archivés intégralement.

Les anciennes passes ne possèdent pas ces instantanés. Leur consultation indique explicitement que l’historique Git est indisponible.

## Interventions

Une consigne envoyée pendant une exécution est conservée pour la prochaine passe et enregistrée sur celle-ci lorsqu’elle est consommée. Une nouvelle consigne remplace la consigne encore en attente. Le chat contextualisé reçoit l’objectif et la mémoire, mais ne modifie pas automatiquement les consignes du moteur.

Le bilan depuis la dernière visite utilise le stockage du navigateur. Il n’est pas synchronisé entre appareils.

## Comptes partagés et progression

Les comptes sont réservés pendant une passe à l’échelle du serveur. Une boucle attend si tous ses comptes sont occupés ; elle peut utiliser un autre compte autorisé. La disponibilité est attribuée au premier passage de l’ordonnanceur après libération, sans préemption. Les délais de disponibilité d’un même compte sont partagés entre boucles. Les détails des autres propriétaires ne sont pas exposés.

La boucle est mise en pause avec une raison lorsqu’un même échec ou une même demande de décision se répète sur trois passes, ou lorsqu’un cycle de modifications est détecté. Une passe de diagnostic sans modification n’est pas une stagnation à elle seule.

Le formulaire guidé suit quatre étapes : projet, objectif et critères, autonomie et limites, agents autorisés.

## Limites connues

Les critères subjectifs demandent une revue humaine. Les étapes cochées par l’agent restent des déclarations. La validation utilisateur enregistre une décision explicite sans fusion ni publication automatique. L’intégration s’effectue depuis la branche ou le dossier affiché dans Modifications.

Le bilan de visite est propre au compte et au navigateur, sans synchronisation entre appareils. Les instantanés n’archivent pas intégralement les sous-modules ou les fichiers ignorés par Git. La mémoire récente est bornée à 24 Ko ; l’historique des passes conserve les bilans antérieurs.

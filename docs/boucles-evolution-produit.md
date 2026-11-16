# Faire de Boucles un espace de travail autonome utile et vérifiable

Document de transmission pour Sol 6.1 — 16 novembre 2026.

## Demande et portée

Faire évoluer l’onglet Boucles de tmuxatlas pour permettre de confier un objectif à des agents, comprendre leurs progrès, examiner leurs résultats et intervenir au bon moment.

Ce document rassemble l’analyse du code et les améliorations proposées. L’analyse initiale a été réalisée sans lancer de boucle et sans test visuel en navigateur. Les constats doivent être revérifiés sur le code au moment de l’implémentation, car le dépôt peut évoluer.

La priorité est la qualité du travail obtenu et sa lisibilité. Le nombre de passes et le temps d’activité restent des informations secondaires.

## État actuel

Le frontend propose déjà :

- un projet actif et un filtre par projet ;
- une liste de boucles et un panneau de détail ;
- plusieurs CLI et comptes autorisés ;
- les commandes démarrer, mettre en pause, reprendre et arrêter ;
- un historique des passes et leurs journaux ;
- des informations Git ;
- des chats rapides ;
- une isolation facultative dans un worktree Git.

Le moteur conserve le même agent disponible entre les passes et change de compte ou de CLI lorsque nécessaire. Il gère les limites d’utilisation et les reprises différées.

Cette base est utile. Le principal manque est que l’interface montre l’activité des agents plus clairement que les améliorations réellement obtenues.

### Sources à examiner

- `web/src/components/loops/LoopsPage.tsx` : page, sélection, formulaire, polling, historique et chats.
- `web/src/components/loops/SubscriptionPool.tsx` : sélection et commandes des comptes.
- `web/src/components/loops/ProjectSelector.tsx` : choix du projet.
- `web/src/components/loops/loopsApi.ts` : contrats frontend/API.
- `web/src/components/loops/loopHelpers.ts` : libellés et présentation des états.
- `pkg/loops/loops.go` : modèles, persistance, création, modification, lecture des logs et chats.
- `pkg/loops/runner.go` : exécution, transmission du contexte, sélection des agents, résumé et conditions de fin.
- `pkg/loops/ratelimit.go` : détection des limites et estimation de la reprise.
- `pkg/loops/http.go` : endpoints.
- `pkg/loops/loops_test.go` et `web/src/components/loops/LoopsPage.test.tsx` : tests existants.

## Direction produit

Conserver le nom **Boucles**, avec le sous-titre :

> Des agents qui avancent sur tes objectifs.

L’utilisateur doit pouvoir répondre rapidement à cinq questions :

1. Quel objectif ai-je confié ?
2. Que fait la boucle maintenant ?
3. Qu’a-t-elle accompli et comment cela a-t-il été vérifié ?
4. Quelle est la prochaine étape ?
5. Une décision de ma part est-elle nécessaire ?

## 1. Définir des objectifs avec une fin vérifiable

### Deux modes explicites

| Mode | Usage | Fin attendue |
| --- | --- | --- |
| Mission | Corriger un problème, réaliser une fonctionnalité, améliorer un parcours | Critères atteints ou limite définie |
| Entretien continu | Traiter progressivement un ensemble d’améliorations | Mise en attente lorsqu’il n’y a plus de travail pertinent |

Le mode continu ne doit pas obliger l’agent à inventer indéfiniment des modifications. Il doit pouvoir indiquer qu’aucun travail pertinent ne reste dans le périmètre. Une reprise doit avoir un déclencheur explicite, par exemple une nouvelle consigne ou un nouveau travail ajouté ; ne pas supposer qu’une planification existe déjà.

### Création d’une boucle

Présenter les choix dans cet ordre :

1. Projet.
2. Objectif et périmètre.
3. Critères de réussite.
4. Mode, limite et autonomie.
5. Agent et comptes autorisés.

Exemple :

> **Objectif :** fiabiliser le parcours d’inscription.  
> **Réussite :** les trois scénarios décrits passent les tests.  
> **Périmètre :** inscription et validation du formulaire.  
> **Limite :** deux heures, puis bilan.

Au moment de l’analyse, le formulaire active par défaut le mode continu et l’exécution autonome, mais désactive le worktree et l’arrêt sur `LOOP_DONE`. Revoir ces choix à partir des deux modes proposés, sans modifier silencieusement les boucles existantes.

## 2. Montrer les résultats avant les logs

Le panneau principal d’une boucle doit présenter :

- **Maintenant** : l’action en cours, si elle est connue.
- **Accompli** : les éléments terminés et ceux restant à traiter.
- **Preuves** : vérifications exécutées, résultats et changements associés.
- **Suite** : la prochaine action prévue.
- **Ton intervention** : une décision précise lorsqu’elle est nécessaire.

Organiser le détail en quatre vues : **Résumé · Modifications · Historique · Logs**.

### Résultat structuré par passe

Aujourd’hui, le résumé est principalement construit à partir de la dernière ligne non vide de sortie, tronquée, complétée par les statistiques Git. Il ne constitue pas un bilan fiable.

Introduire un résultat structuré comprenant au minimum :

- problème ou élément du plan traité ;
- changement réalisé ;
- vérifications et résultats ;
- limites, échecs ou incertitudes ;
- prochaine étape ;
- éventuelle décision demandée.

Conserver les logs bruts. Prévoir le cas où le CLI ne produit pas le format attendu : afficher un résultat incomplet, sans inventer de succès ni perdre les informations disponibles.

Distinguer les déclarations de l’agent des faits observés par Atlas. Une vérification annoncée dans un résumé n’a pas la même valeur qu’une commande exécutée et enregistrée par le moteur.

## 3. Construire une mémoire persistante de boucle

Chaque passe relance actuellement un processus avec l’objectif et le dernier résumé. La mémoire explicitement transmise entre les passes reste courte.

Ajouter une mémoire persistante comprenant :

- le plan et les éléments terminés ;
- les décisions prises ;
- les tentatives échouées et leurs causes ;
- les vérifications déjà réalisées ;
- les prochaines actions utiles ;
- les consignes nouvelles de l’utilisateur.

Transmettre cette mémoire lors d’un changement de compte ou de CLI. La bascule doit permettre une véritable transmission du travail.

Cette mémoire doit rester de taille maîtrisée et survivre à un redémarrage du serveur. Définir clairement sa mise à jour et son lien avec l’historique des passes.

### Détection de stagnation

Détecter progressivement les situations telles que :

- répétition du même échec ;
- mêmes modifications annulées puis réappliquées ;
- absence répétée d’avancée sur les critères.

Exposer la raison de la détection. Une absence de changement de fichier ne suffit pas, à elle seule, à conclure à une stagnation : une passe de diagnostic ou de vérification peut être utile.

En cas de stagnation confirmée, produire un bilan et demander une orientation précise.

## 4. Distinguer fin d’exécution et objectif atteint

Actuellement, atteindre le nombre maximal de passes ou recevoir `LOOP_DONE` peut conduire au même statut « Terminée ». Les tests sont demandés dans le prompt, mais le moteur ne possède pas d’étape indépendante de validation du résultat.

### États à rendre visibles

| État | Signification |
| --- | --- |
| En cours | L’agent travaille |
| En vérification | Les contrôles configurés s’exécutent |
| À examiner | Un résultat est disponible pour revue |
| Objectif atteint | Les critères définis sont satisfaits |
| Limite atteinte | Le budget de temps ou de passes est épuisé |
| Bloquée | Une décision ou une correction est nécessaire |

Conserver les états opérationnels utiles : prête, en pause, arrêtée, en attente de disponibilité et erreur. L’implémentation peut séparer état d’exécution et résultat métier plutôt que multiplier les valeurs d’un seul champ ; choisir un modèle clair et compatible avec les données existantes.

Pour les critères automatisables, Atlas peut exécuter les contrôles configurés et enregistrer commande, résultat et durée. Pour les critères subjectifs, comme la qualité d’une interface, conserver une validation humaine explicite.

`LOOP_DONE` doit être traité comme une déclaration de l’agent à interpréter au regard des critères, pas comme une preuve universelle de réussite.

Une limite atteinte doit produire un bilan du travail accompli et restant, sans afficher un faux succès.

## 5. Faire de la revue des changements une fonction centrale

Proposer le worktree par défaut pour les nouvelles missions qui modifient du code, lorsque le projet Git le permet. Expliquer simplement où les modifications seront produites.

Créer un parcours de sortie :

> Voir les changements → comprendre les vérifications → préparer leur intégration.

Afficher le vrai diff, les fichiers concernés et les preuves pertinentes. Ne pas assimiler la fin d’une mission à une autorisation automatique de fusionner ou de publier.

### Historique Git fidèle à chaque passe

Constat actuel : sélectionner une ancienne passe charge ses logs, mais les informations Git sont calculées sur le dossier actuel. Elles ne représentent donc pas nécessairement les modifications de cette passe.

Enregistrer une référence ou un instantané avant/après chaque exécution. Couvrir les changements suivis, indexés et non suivis, ainsi que les commits réalisés pendant la passe. Ne pas dépendre uniquement de `git diff` sur le dossier courant.

Les anciennes passes sans instantané doivent être identifiées comme telles. Ne pas leur attribuer rétrospectivement le diff actuel.

## 6. Alléger l’onglet et améliorer les interventions

### Organisation proposée

| Zone | Contenu |
| --- | --- |
| En haut | Projet, boucles actives, décisions en attente, création |
| À gauche | Boucles classées par attention nécessaire, puis activité |
| Au centre | Objectif, avancement, résultat et prochaine étape |
| Dans les détails | Comptes autorisés, modèles, réglages et logs |

Éviter que les réglages des comptes occupent l’espace principal quand aucune intervention n’est nécessaire. Conserver leur accès rapide et leur état visible de manière compacte.

Prévoir une navigation utilisable sur mobile et un historique qui reste lisible après de nombreuses passes.

### Discuter de cette boucle

Transformer les chats rapides en action contextuelle : **Discuter de cette boucle**.

Transmettre l’objectif, le bilan, le contexte pertinent et le bon dossier. Aujourd’hui, ouvrir un chat dans le dossier ne garantit pas cette transmission.

Clarifier l’effet du chat sur la boucle : une conversation ne doit pas être présentée comme une consigne appliquée au moteur si ce transfert n’a pas eu lieu.

### Donner une consigne pour la prochaine passe

Permettre d’ajouter une consigne sans devoir modifier le prompt pendant l’exécution.

- Montrer la consigne en attente.
- Indiquer qu’elle s’appliquera à la prochaine passe.
- Confirmer son application dans l’historique.
- Ne pas annoncer qu’elle a modifié une passe déjà démarrée.

### Bilan depuis la dernière visite

Ajouter un bilan qui montre :

- ce qui a avancé ;
- ce qui a été vérifié ;
- ce qui attend une décision ;
- les résultats prêts à examiner.

Ce bilan est particulièrement cohérent avec la promesse d’Atlas : signaler quand l’attention de l’utilisateur devient utile.

Définir un repère de consultation par utilisateur afin d’éviter de confondre l’activité nouvelle avec toute l’activité historique.

## Corrections immédiates identifiées

### Cohérence du filtre projet

Dans `LoopsPage.tsx`, la boucle sélectionnée est recherchée dans la liste complète avant le repli sur la liste filtrée. Le détail d’une boucle extérieure au filtre peut donc rester affiché.

Faire en sorte que la sélection appartienne à la liste visible. Lors d’un changement de filtre, sélectionner une boucle pertinente ou afficher l’état vide.

### Libellés de limites d’utilisation

L’interface utilise fréquemment « limite 5h », alors que le moteur détecte plusieurs types de limites et utilise parfois un délai de réessai estimé.

Employer des libellés fidèles aux données disponibles, par exemple :

> Compte temporairement indisponible · nouvel essai dans 12 min.

Distinguer une heure de disponibilité annoncée d’une estimation de nouvelle tentative. Ne pas promettre qu’un compte sera disponible à l’expiration d’un délai estimé.

## Plan d’implémentation recommandé

### Lot 1 — Compréhension et cohérence

- Corriger la sélection sous filtre projet.
- Corriger les libellés de limites et de reprise estimée.
- Distinguer limite atteinte, arrêt et réussite dans les bilans.
- Réorganiser le détail autour du résumé, des modifications, de l’historique et des logs.
- Introduire le contrat de résultat structuré avec repli pour les sorties non conformes.

**Résultat attendu :** comprendre ce qui a été accompli sans devoir lire tout le journal.

### Lot 2 — Travail cumulatif et vérifiable

- Ajouter les modes Mission et Entretien continu.
- Ajouter critères de réussite, périmètre et limites.
- Mettre en place la mémoire persistante.
- Enregistrer les vérifications et distinguer faits observés et déclarations.
- Introduire une première détection de stagnation explicable.

**Résultat attendu :** chaque passe poursuit un travail identifié, avec des critères de fin cohérents.

### Lot 3 — Revue et récupération des résultats

- Proposer l’isolation Git pour les nouvelles missions de code.
- Enregistrer les changements propres à chaque passe.
- Afficher un diff historique fiable.
- Présenter un bilan final et les actions de préparation à l’intégration.

**Résultat attendu :** pouvoir examiner et récupérer un résultat exploitable.

### Lot 4 — Pilotage quotidien

- Ajouter les consignes pour la prochaine passe.
- Ajouter le chat contextualisé.
- Ajouter le bilan depuis la dernière visite.
- Améliorer la gestion globale des comptes lorsque plusieurs boucles les utilisent, en définissant explicitement les règles de disponibilité et de priorité.

**Résultat attendu :** piloter plusieurs boucles sans surveillance permanente des logs.

## Consignes de réalisation pour Sol 6.1

1. Relire les instructions du dépôt et examiner le code actuel avant toute modification.
2. Respecter les changements déjà présents dans l’espace de travail.
3. Implémenter progressivement les lots, en conservant un parcours fonctionnel à chaque étape.
4. Réutiliser les composants UI et conventions du projet. Découper `LoopsPage.tsx` lorsque les responsabilités ajoutées le justifient.
5. Prévoir la migration et les valeurs de repli pour les boucles persistées avant cette évolution. Ne pas les relancer automatiquement ni changer silencieusement leur mode.
6. Ne pas lancer de vraies boucles autonomes ou consommer des comptes pour les tests ; utiliser les moyens de simulation pertinents.
7. Tester les transitions d’état, la persistance et les échanges API, ainsi que les parcours utilisateur modifiés.
8. Actualiser la documentation pour qu’elle reflète le rôle réel de Boucles. Le README décrit encore l’orchestration d’agents comme un non-objectif : réconcilier cette formulation avec le périmètre effectivement implémenté.
9. Dans le compte rendu, préciser les fonctionnalités terminées, les validations réalisées et les limites restantes.

### Scénarios de validation importants

- Changer de projet avec un filtre actif ne laisse pas le détail d’une boucle hors filtre.
- Atteindre le maximum de passes n’affiche pas « objectif atteint » sans preuve.
- Une sortie agent invalide conserve les logs et n’invente pas de résultat.
- Un changement de CLI transmet la mémoire de la boucle.
- Un redémarrage conserve les résultats et le contexte sans prétendre qu’un processus interrompu travaille encore.
- Consulter une ancienne passe affiche ses changements ou signale leur indisponibilité.
- Une consigne ajoutée pendant une passe s’applique une seule fois au bon moment.
- Un délai estimé est présenté comme une prochaine tentative, pas comme une disponibilité certaine.
- Une passe de diagnostic utile sans modification de fichier n’est pas automatiquement classée comme stagnante.

Pour les tests Go qui utilisent tmux, respecter les précautions du dépôt : retirer `TMUX` de l’environnement et utiliser un `TMUX_TMPDIR` de test isolé. Exécuter les contrôles adaptés aux modifications et les vérifications requises par le projet.

## Critère de réussite produit

En revenant dans Boucles après une absence, l’utilisateur doit comprendre rapidement :

> Voilà ce qui a avancé, voilà les preuves, voilà ce qu’il reste à faire et voilà la décision éventuelle que je dois prendre.

L’onglet aura atteint son objectif lorsque cette compréhension ne nécessitera plus de reconstruire le travail à partir des journaux des agents.

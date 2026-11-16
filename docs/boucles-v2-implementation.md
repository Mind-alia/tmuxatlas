# État de l’implémentation V2

## Corrections réalisées

- Les résultats des contrôles Atlas sont explicitement transmis à la passe suivante, séparément des déclarations agents.
- Les étapes ne sont plus cochées avant qualification de la passe. L’agent peut proposer une étape pour revue ; une déclaration ne vaut pas preuve.
- Un contrôle échoué garde sa priorité sur l’attente de nouveau travail en mode entretien.
- Les contrôles restent visibles même sans bilan agent valide.
- Les limites de temps et de passes ont des motifs distincts.
- La revue enregistre auteur, date, passe examinée et critères textuels.
- Les consignes utilisateur sont conservées dans une liste durable indépendante de la fenêtre de mémoire récente.
- Le plan accepte des états, des résultats attendus et des dépendances. Les dépendances inconnues et cycliques sont refusées.
- La prochaine passe cible une étape éligible. Les anciennes étapes `done` restent des déclarations à revoir.
- Une mission sans étape exécutable s’arrête pour revue ou blocage au lieu de chercher indéfiniment une autre modification.
- L’historique affiche résultats et décisions séparément des logs.
- Deux boucles ne peuvent pas réserver simultanément le même dossier canonique de travail.
- Les commandes de consigne, revue et plan restaurent leur état en mémoire si la sauvegarde échoue.

## Périmètre encore à réaliser du document complet

La V2 n’est pas intégralement terminée. Restent : état initial et comparaison fiable des régressions, contrôles par étape, priorité et équité avec vieillissement, revue indépendante optionnelle, revue détaillée avec réserves et refus, invalidation explicite des validations après modification, édition UI complète des dépendances et critères, journal versionné des consignes, chronologie paginée, stockage séparé des artefacts, comparaison globale du diff, mesures de charge et indicateurs d’efficacité, tests navigateur de bout en bout.

Les fonctions ci-dessus sont une évolution incrémentale du système existant. La présence de tests unitaires et d’un build réussi ne signifie pas que les critères restants sont satisfaits.

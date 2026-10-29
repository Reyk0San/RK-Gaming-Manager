# Publication GHCR et récupération sur TrueNAS SCALE

Le workflow `.github/workflows/docker-ghcr.yml` publie exclusivement
`ghcr.io/reyk0san/rk-gaming-manager`, pour `linux/amd64` (Intel TrueNAS SCALE).
Il part de `develop` après fusion de la PR #3 Steam, commit `9ebb077`.
La création de la branche et de la PR ne lance aucune publication.

## Validation puis publication manuelle

1. Relire et valider la PR, puis la fusionner volontairement dans `develop`.
2. Vérifier les restrictions ci-dessous. Le workflow doit aussi être présent sur
   la branche par défaut pour être déclenchable ; cette PR ne modifie pas `main`.
3. Après validation, ouvrir **Actions → Publish RK Gaming Manager to GHCR →
   Run workflow**, sélectionner **develop**, puis cocher `confirm_publish`.
   Cette action publie réellement ; la confirmation est désactivée par défaut.
4. Consulter le résumé de l'exécution réussie et conserver le commit complet
   et la référence `ghcr.io/reyk0san/rk-gaming-manager@sha256:…`.

Aucun déclencheur `push`, `pull_request` ou `pull_request_target` n'est présent.
Le job refuse toute branche autre que `develop` et tout autre dépôt. Protéger
`develop` par des revues obligatoires et empêcher les pushes directs pour que
seul du code approuvé y entre. La confirmation manuelle ne remplace pas ces règles.
Les règles de protection et les paramètres Actions ne sont pas modifiés par cette PR.

Deux tags seulement : `develop` et `sha-<SHA Git complet>`. Aucun `latest`.
`develop` est mobile ; une nouvelle exécution d'un même commit peut aussi remplacer
son tag SHA, car les images de base et certains paquets du Dockerfile sont mobiles.
**Seule la référence par digest fixe exactement l'image à récupérer**, tant que
la version du package reste conservée. Il s'agit de reproductibilité du déploiement,
pas d'une garantie de reconstruction bit à bit.
Le digest provient de Buildx, est vérifié comme SHA256 puis exposé dans le résumé
Actions et dans la sortie `digest` du job. Avec la provenance Buildx, il peut
identifier un index OCI incluant l'image amd64 et ses métadonnées d'attestation.

## Build et workflows existants

La cible `runtime` du Dockerfile existant construit React avec Node 24,
`npm ci`, Vite et TypeScript, puis les binaires Go `nexorious` et `nexctl`
avec Go 1.26 et `CGO_ENABLED=0`. Buildx impose `linux/amd64` sur un runner
Ubuntu x86_64 ; aucune émulation QEMU n'est nécessaire. Le frontend construit
est copié avant sa compilation embarquée dans Go. Les exceptions de `.dockerignore`
conservent les guides embarqués. Aucun secret applicatif n'est nécessaire au build.
Les arguments `VERSION=develop` et `COMMIT` renseignent la version des binaires.

Le workflow historique `release-artifacts.yaml` utilise `runtime-ci`, des binaires
préconstruits et publie lors des releases vers `ghcr.io/drzero42/nexorious`, avec
`latest`. Ses builds de PR utilisent `push: false`. Il reste inchangé et ne publie
pas notre nouvelle image. Ne pas l'utiliser pour publier RK Gaming Manager.
Les workflows de tests principaux ciblent actuellement `main` : une PR vers
`develop` ne garantit donc pas leur exécution. Le nouveau workflow construit
Go/React mais ne remplace pas les suites de tests applicatifs existantes.
Les fichiers Compose existants pointent également vers l'image amont et ne sont
pas modifiés ; ne pas les appliquer aveuglément à une installation existante.

## Permissions et restrictions GitHub / GHCR

- Authentification du push uniquement avec `secrets.GITHUB_TOKEN`, fourni
  automatiquement par Actions ; aucun PAT ni secret supplémentaire à créer.
- Permissions déclarées : `contents: read`, `packages: write` ; toutes les autres
  permissions du token sont désactivées. Checkout ne conserve pas ses identifiants.
- Actions officielles épinglées sur les SHA des releases checkout v7.0.1,
  setup-buildx v4.4.1, login v4.7.0, build-push v7.4.0. Mettre à jour ces SHA
  régulièrement après revue. Le runner GitHub hébergé fournit le runtime requis.
- Dans **Settings → Actions → General**, Actions doit être activé et les politiques
  de dépôt/organisation doivent autoriser ces actions et l'écriture de packages.
  Vérifier aussi les quotas/minutes, stockage de cache et accès aux registres
  Docker Hub, npm, Go, Alpine et PyPI utilisés par le Dockerfile.
- GitHub exige la présence du workflow sur la branche par défaut (`main` actuellement)
  pour accepter `workflow_dispatch`, y compris via API/CLI. Après la seule fusion
  vers `develop`, le bouton peut être absent ou le dispatch refusé. Prévoir une
  intégration approuvée du workflow sur la branche par défaut ou un changement
  approuvé de branche par défaut ; aucun de ces changements n'est effectué ici.
  Sélectionner ensuite explicitement `develop` ; `main` est refusé par le job.
- Un package nouvellement publié est normalement **privé**, même pour un dépôt public.
  Dans le profil GitHub → **Packages → rk-gaming-manager → Package settings**,
  vérifier le dépôt lié `Reyk0San/RK-Gaming-Manager`, l'héritage des droits et
  **Manage Actions access** : ce dépôt doit avoir le droit **Write**. C'est notamment
  nécessaire si le package existait déjà sans association au dépôt. Le label OCI
  `source` facilite cette association. Un `permission_denied: write_package` exige
  de vérifier ces droits et les politiques Actions ; ne pas ajouter de PAT au workflow.
- Pour des pulls anonymes sur TrueNAS, rendre volontairement le package **Public**
  après examen de son contenu. La visibilité n'est pas changée par ce workflow.
  Pour un package privé, GHCR exige une authentification de lecture sur le client
  externe : généralement un PAT **classic** limité à `read:packages`, appartenant
  à un compte autorisé (et autorisé SSO si applicable). Le `GITHUB_TOKEN` éphémère
  du job ne doit jamais être copié sur le NAS. Si aucun PAT n'est souhaité sur le
  NAS non plus, utiliser un package public.

Ne jamais afficher de token, activer `set -x` avec des identifiants, ni transmettre
les secrets applicatifs en arguments de build. Ne pas ajouter de `.env` réel au
suivi Git ; le contexte de build provient d'un checkout propre. Le cache peut
contenir les sources et dépendances du build, jamais des secrets de production.

## Récupérer l'image sur TrueNAS sans toucher aux applications

La publication ne déploie rien sur le NAS. Les commandes suivantes téléchargent
ou inspectent une image ; elles ne démarrent aucun conteneur et ne modifient aucun
volume. Sur une version de SCALE disposant de Docker (24.10 ou plus récente),
utiliser un shell autorisé et remplacer le digest d'exemple par celui du résumé :

```sh
# Package public : aucune connexion nécessaire.
docker pull --platform linux/amd64 ghcr.io/reyk0san/rk-gaming-manager@sha256:<64-caracteres-hexadecimaux>
docker image inspect ghcr.io/reyk0san/rk-gaming-manager@sha256:<64-caracteres-hexadecimaux> \
  --format '{{.Os}}/{{.Architecture}} {{json .RepoDigests}}'
```

Pour un package privé, configurer les identifiants de registre dans l'interface
TrueNAS adaptée à la version, ou se connecter au préalable dans le shell avec
`docker login ghcr.io --username <compte-github>` et saisir le token uniquement
à l'invite de mot de passe. Ne pas le mettre dans la ligne de commande, un fichier
Compose suivi par Git ou un journal. Un login CLI peut enregistrer l'identifiant
localement et n'alimente pas nécessairement les identifiants de l'interface Apps ;
utiliser `docker logout ghcr.io` après une vérification ponctuelle si approprié.

SCALE 24.04 et antérieur utilise Kubernetes : ne pas installer Docker sur le NAS
pour suivre ces commandes. Utiliser la gestion d'images/registre adaptée à cette
version ou vérifier l'image sur une autre machine Docker.

Pour un futur déploiement, préparer séparément une application de validation avec
la référence par digest, un port et des datasets dédiés. Le runtime expose 8000
et utilise UID/GID 10001 ; PostgreSQL, variables d'environnement, droits de stockage
et migrations restent à configurer selon la documentation applicative. Ne pas
réutiliser les volumes de production pour un essai. Aucun `compose up/down`,
redémarrage d'application, migration ou changement de dataset existant n'est
nécessaire pour simplement récupérer l'image.

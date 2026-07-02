# "Solo Codium crea paneles" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Que solo logins de GitHub autorizados obtengan sesión (y por tanto puedan crear paneles), mientras cualquiera puede abrir salas/escenas ya existentes.

**Architecture:** Dos gates que se refuerzan. (1) Backend: el callback de OAuth rechaza logins fuera de `ALLOWED_CREATORS` (no emite token, redirige a `?auth_error=unauthorized`). (2) Frontend: al cargar, un usuario sin token solo puede acceder a salas/escenas existentes; el lienzo en blanco / sala inexistente redirige a `/auth/login`. El frontend se toca vía patch aplicado en build (submódulo intacto), igual que el parche de imágenes de sala.

**Tech Stack:** Go (chi, golang-jwt), React/TypeScript (Excalidraw fork, Vite), Docker, GitHub Actions.

## Global Constraints

- Enfoque **gate blando**: aceptable que sea esquivable fabricando peticiones a mano.
- **Anti-lockout:** `ALLOWED_CREATORS` vacía/sin definir ⇒ restricción desactivada (todos permitidos).
- Comparación de login **case-insensitive** y tolerante a espacios alrededor de comas.
- Frontend: NO forkear el submódulo; el cambio va como patch aplicado en `excalidraw-complete.Dockerfile` con `git apply` **tras `rm -f .git`**.
- No romper enlaces de escena compartida existentes (`#json=`, `#url=`).
- No branches ni PRs: commits directos a `main` de cada repo.
- Repos: `CodiumTeam/excalidraw-full` (código+imagen), `CodiumTeam/excalidraw` (deploy/compose).

---

### Task 1: Backend — gate de login por lista blanca (TDD)

**Files:**
- Modify: `handlers/auth/auth.go` (añadir `isLoginAllowed`; usar en `HandleCallback`; asegurar import `strings`)
- Test: `handlers/auth/auth_test.go` (crear)

**Interfaces:**
- Produces: `func isLoginAllowed(login string) bool` (no exportada, misma package `auth`).

- [ ] **Step 1: Write the failing test**

Crear `handlers/auth/auth_test.go`:

```go
package auth

import "testing"

func TestIsLoginAllowed(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		login string
		want  bool
	}{
		{"empty env allows all", "", "anyone", true},
		{"listed login allowed", "luisrovirosa", "luisrovirosa", true},
		{"unlisted login denied", "luisrovirosa", "someoneelse", false},
		{"case-insensitive", "LuisRovirosa", "luisrovirosa", true},
		{"spaces around commas", " luisrovirosa , bob ", "bob", true},
		{"multi list denies outsider", "a,b,c", "d", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ALLOWED_CREATORS", tc.env)
			if got := isLoginAllowed(tc.login); got != tc.want {
				t.Fatalf("isLoginAllowed(%q) env=%q = %v, want %v", tc.login, tc.env, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./handlers/auth/ -run TestIsLoginAllowed -v`
Expected: FAIL con `undefined: isLoginAllowed`.

- [ ] **Step 3: Add `isLoginAllowed` and ensure `strings` import**

En `handlers/auth/auth.go`, en el bloque de imports añadir `"strings"` si no está. Añadir la función (a nivel de package, p.ej. al final del fichero):

```go
// isLoginAllowed reports whether a GitHub login may obtain a session. The
// allow-list comes from ALLOWED_CREATORS (comma-separated logins). Empty/unset
// disables the restriction (anti-lockout): everyone is allowed.
func isLoginAllowed(login string) bool {
	raw := strings.TrimSpace(os.Getenv("ALLOWED_CREATORS"))
	if raw == "" {
		return true
	}
	for _, entry := range strings.Split(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(entry), strings.TrimSpace(login)) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./handlers/auth/ -run TestIsLoginAllowed -v`
Expected: PASS (6 subtests).

- [ ] **Step 5: Wire the check into `HandleCallback`**

En `handlers/auth/auth.go`, en `HandleCallback` (GitHub), justo **después** de `json.Unmarshal(body, &githubUser)` (y antes de construir el `user`/`createJWT`), insertar:

```go
	if !isLoginAllowed(githubUser.Login) {
		logrus.Warnf("login not allowed to create: %s", githubUser.Login)
		http.Redirect(w, r, "/?auth_error=unauthorized", http.StatusTemporaryRedirect)
		return
	}
```

- [ ] **Step 6: Verify it builds**

Run: `mkdir -p frontend && printf '<!doctype html>' > frontend/index.html && go build ./...`
Expected: exit 0 (sin errores). (El stub `frontend/` satisface `//go:embed all:frontend`; no comitear ese stub — el repo ya trae `frontend/.keep`.)

- [ ] **Step 7: Commit**

```bash
git checkout -- frontend/.keep 2>/dev/null; rm -f frontend/index.html
git add handlers/auth/auth.go handlers/auth/auth_test.go
git commit -m "auth: gate login por ALLOWED_CREATORS (rechaza logins no autorizados)"
```

---

### Task 2: Frontend — gate de acceso (patch en build)

**Files:**
- Modify (working tree del submódulo, para generar patch): `excalidraw/excalidraw-app/App.tsx`
- Create: `create-gate-frontend.patch` (en la raíz de `excalidraw-full`)
- Modify: `excalidraw-complete.Dockerfile` (aplicar el patch nuevo)

**Interfaces:**
- Consumes: `getCollaborationLinkData` (ya importado de `./data`), `loadFromFirebase` (de `./data/firebase`), `setErrorMessage` (setter existente en el componente App).
- Produces: patch `create-gate-frontend.patch` aplicado en build.

- [ ] **Step 1: Inicializar el submódulo en el commit fijado**

```bash
git submodule update --init excalidraw
```
Expected: checkout a `b5cca508…` (o el commit fijado en `.gitmodules`).

- [ ] **Step 2: Añadir `loadFromFirebase` al import existente**

En `excalidraw/excalidraw-app/App.tsx`, línea ~142, cambiar:
```ts
import { loadFilesFromFirebase } from "./data/firebase";
```
por:
```ts
import { loadFilesFromFirebase, loadFromFirebase } from "./data/firebase";
```

- [ ] **Step 3: Añadir los helpers del gate a nivel de módulo**

En `excalidraw/excalidraw-app/App.tsx`, insertar estas funciones a nivel de módulo (p.ej. justo antes de `const initializeScene = async` en la línea ~244):

```ts
// Codium access gate: usuarios sin sesión solo pueden abrir contenido EXISTENTE
// (salas/escenas compartidas). El lienzo en blanco o una sala inexistente exigen
// login. Gate blando (nivel de app).
const isLoggedIn = (): boolean => {
  const token = localStorage.getItem("token");
  if (!token) {
    return false;
  }
  try {
    const payload = JSON.parse(atob(token.split(".")[1]));
    return typeof payload.exp === "number" && payload.exp * 1000 > Date.now();
  } catch {
    return false;
  }
};

const enforceCodiumAccessGate = async (
  setErrorMessage: (msg: string) => void,
): Promise<boolean> => {
  const params = new URLSearchParams(window.location.search);
  if (params.get("auth_error") === "unauthorized") {
    setErrorMessage(
      "Tu cuenta de GitHub no está autorizada para crear paneles en Codium.",
    );
    params.delete("auth_error");
    const qs = params.toString();
    window.history.replaceState(
      {},
      document.title,
      window.location.pathname + (qs ? `?${qs}` : "") + window.location.hash,
    );
    return false;
  }
  if (isLoggedIn()) {
    return true;
  }
  const roomLinkData = getCollaborationLinkData(window.location.href);
  if (roomLinkData) {
    try {
      const scene = await loadFromFirebase(
        roomLinkData.roomId,
        roomLinkData.roomKey,
        null,
      );
      if (scene) {
        return true;
      }
    } catch {
      // no se pudo cargar → tratar como inexistente
    }
  }
  const hash = window.location.hash;
  if (/^#json=/.test(hash) || /^#url=/.test(hash)) {
    return true;
  }
  window.location.href = "/auth/login";
  return false;
};
```

- [ ] **Step 4: Llamar al gate al inicio de `loadCanvas`**

En `excalidraw/excalidraw-app/App.tsx`, en el cuerpo de `const loadCanvas = async () => {` (línea ~607), insertar como **primeras líneas** del cuerpo:

```ts
      if (!(await enforceCodiumAccessGate(setErrorMessage))) {
        return;
      }
```

- [ ] **Step 5: Generar el patch y verificar que aplica limpio**

```bash
cd excalidraw
git diff > ../create-gate-frontend.patch
git stash
git apply --check ../create-gate-frontend.patch && echo "APLICA LIMPIO"
git stash pop
git checkout -- excalidraw-app/App.tsx
cd ..
```
Expected: imprime `APLICA LIMPIO`. (Se revierte el working tree del submódulo: el mecanismo es patch-en-build, submódulo intacto.)

- [ ] **Step 6: Aplicar el patch en el Dockerfile**

En `excalidraw-complete.Dockerfile`, tras la línea que aplica el patch de imágenes (`RUN cd excalidraw && rm -f .git && git apply ../room-files-frontend.patch`), añadir el copy+apply del nuevo patch. Debe copiarse el fichero al contexto y aplicarse sobre el submódulo ya sin `.git`:

```dockerfile
COPY create-gate-frontend.patch ./
RUN cd excalidraw && git apply ../create-gate-frontend.patch
```
(Colocarlo **después** del `rm -f .git && git apply ../room-files-frontend.patch`, para que `.git` ya esté eliminado.)

- [ ] **Step 7: Commit**

```bash
git add create-gate-frontend.patch excalidraw-complete.Dockerfile
git commit -m "frontend: gate de acceso (anon solo salas/escenas existentes) via patch en build"
```

- [ ] **Step 8: Verificar el build de imagen (esta es la prueba real del frontend)**

Tras el push (Task 3), observar el workflow "Build and Push Docker Image" de `CodiumTeam/excalidraw-full`:
```bash
gh run watch <run-id> --repo CodiumTeam/excalidraw-full --exit-status
```
Expected: verde. El `git apply` + `pnpm build` con ambos patches deben pasar. Si `git apply` falla, revisar el orden en el Dockerfile (debe ir tras `rm -f .git`).

---

### Task 3: Config, deploy y verificación en producción

**Files:**
- Modify: `docker-compose.yml` (repo `CodiumTeam/excalidraw`, servicio `excalidraw`)

**Interfaces:**
- Consumes: imagen `ghcr.io/codiumteam/excalidraw-full:latest` reconstruida en Tasks 1-2.

- [ ] **Step 1: Push de excalidraw-full a main (dispara build)**

En el repo `excalidraw-full`:
```bash
git push origin main
```
Luego ejecutar Task 2 / Step 8 (watch del build) y esperar verde.

- [ ] **Step 2: Añadir `ALLOWED_CREATORS` al compose de deploy**

En `docker-compose.yml` del repo de deploy, en `services.excalidraw.environment`, añadir:
```yaml
      ALLOWED_CREATORS: "luisrovirosa"
```

- [ ] **Step 3: Commit + push del deploy repo (dispara deploy)**

```bash
git add docker-compose.yml
git commit -m "deploy: ALLOWED_CREATORS=luisrovirosa (solo Codium crea paneles)"
git push origin main
```

- [ ] **Step 4: Esperar el deploy**

```bash
gh run watch <deploy-run-id> --repo CodiumTeam/excalidraw --exit-status
```
Expected: verde. (Si no dispara solo, `gh workflow run deploy.yml`.)

- [ ] **Step 5: Verificación manual en producción**

Comprobar cada caso:
1. Anónimo (incógnito), URL de una sala **existente** → carga la sala, sin redirect. ✅
2. Anónimo, `https://draw.codium.team` (sin `#room=`) → redirige a `/auth/login`. ✅
3. Anónimo, `#room=` de una sala **inexistente** → redirige a login. ✅
4. Login con `luisrovirosa` → entra, puede crear (Share → Start session). ✅
5. Login con otra cuenta de GitHub → vuelve con mensaje "cuenta no autorizada", sin token, y **no** entra en bucle. ✅
6. Anónimo, enlace de escena compartida `#json=` existente → carga (no roto). ✅

- [ ] **Step 6: (opcional) Confirmar env en el contenedor**

```bash
ssh ubuntu@152.228.140.187 'cd /opt/excalidraw && sudo docker compose exec excalidraw printenv ALLOWED_CREATORS'
```
Expected: `luisrovirosa`.

---

## Notas de verificación

- Backend con TDD real (`go test ./handlers/auth/`).
- Frontend: la verificación es build verde + pruebas manuales en prod (no hay harness de test del SPA; consistente con cómo se validó el parche de imágenes).
- El orden de effects en React garantiza que `useAuth` guarda el `?token=` en `localStorage` **antes** de que corra el gate (useAuth se declara antes del effect de init).

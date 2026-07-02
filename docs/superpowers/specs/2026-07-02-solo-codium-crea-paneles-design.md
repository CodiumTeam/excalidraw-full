# Diseño: "Solo Codium crea paneles"

Fecha: 2026-07-02
Repos: `CodiumTeam/excalidraw-full` (imagen) + `CodiumTeam/excalidraw` (deploy)

## Objetivo
Que solo la gente autorizada de Codium pueda **crear** paneles (salas de
colaboración `#room=`), mientras cualquiera pueda **abrir/ver/editar** una sala
**existente** compartida por URL.

Enfoque: **gate blando** (a nivel de app + login, no criptográficamente
inviolable). Un usuario técnico fabricando peticiones a mano podría saltárselo;
el objetivo es impedir la creación por externos/curiosos por las vías normales.

Se logra combinando **dos mecanismos** (una sola entrega):

1. **Gate de acceso (frontend):** los usuarios **no logueados** solo pueden
   acceder a salas que **ya existen**. Para el lienzo en blanco / crear, hay que
   estar logueado.
2. **Gate de login (backend):** en el callback de OAuth, si el login de GitHub no
   está en una **lista blanca**, se rechaza (no se emite token). Así solo la gente
   autorizada obtiene sesión → solo ella pasa el gate de acceso y puede crear.

## Comportamiento (tabla de acceso al cargar la app)

| Situación | Resultado |
|---|---|
| Logueado (token válido; solo se emite a autorizados) | Acceso normal (puede crear) |
| No logueado + URL con `#room=` de sala que **existe** | Acceso anónimo (ver/editar) |
| No logueado + enlace de escena compartida existente (`?id=` / `#json=`) | Acceso anónimo (no romper "export to link") |
| No logueado + **sin** `#room=` (app en blanco) | Redirige a login (`/auth/login`) |
| No logueado + `#room=` de sala que **no existe** | Redirige a login |
| Vuelve del login **no autorizado** (`?auth_error=unauthorized`) | Muestra mensaje "cuenta no autorizada"; **no** re-redirige |

## Componente 1 — Gate de acceso (frontend)

Parche de build sobre el submódulo `BetterAndBetterII/excalidraw` (mismo mecanismo
que el parche de imágenes: `git apply` en `excalidraw-complete.Dockerfile` tras
`rm -f .git`).

Reutiliza lo existente:
- Login en cliente: `localStorage.token` + expiración (lógica de `hooks/useAuth.ts`).
- Detección de sala: `getCollaborationLinkData(window.location.href)` (`data/index.ts`).
- Existencia de sala: `loadFromFirebase(roomId, roomKey)` (`data/firebase.ts`) →
  `null` si no existe (usa el `batchGet` anónimo ya existente).
- Redirect a login: `window.location.href = "/auth/login"`.

**Punto de enganche:** en `excalidraw-app/App.tsx`, al inicio del effect que llama
a `initializeScene` (~línea 613), antes de inicializar escena/colaboración:

```
async function enforceAccessGate(): Promise<boolean> {
  if (hasAuthError()) {                 // ?auth_error=unauthorized en la URL
    showUnauthorizedMessage();          // aviso "cuenta no autorizada"
    clearAuthErrorParam();              // limpia el query param
    return false;                       // NO redirige (evita bucle)
  }
  if (isLoggedIn()) return true;        // token válido → pasa
  const roomLinkData = getCollaborationLinkData(window.location.href);
  const hasSharedScene = /* ?id= o #json= presente */;
  if (roomLinkData) {
    const exists = await roomSceneExists(roomLinkData); // loadFromFirebase != null
    if (exists) return true;            // sala existente → anónimo OK
  } else if (hasSharedScene) {
    return true;                        // escena compartida existente → OK
  }
  window.location.href = "/auth/login"; // si no, a login
  return false;
}
```

- `isLoggedIn()`: token en `localStorage` válido y no expirado (misma comprobación
  que `useAuth.ts`; extraer helper reutilizable).
- `roomSceneExists(roomLinkData)`: `(await loadFromFirebase(roomId, roomKey, null)) != null`.
- Si devuelve `false`, no se llama a `initializeScene`.

## Componente 2 — Gate de login (backend)

En `handlers/auth/auth.go`, `HandleCallback` (GitHub), tras obtener `githubUser`:
- Leer lista blanca de la env `ALLOWED_CREATORS` (logins separados por coma).
- **Regla anti-lockout:** si `ALLOWED_CREATORS` está vacía/sin definir ⇒
  restricción **desactivada** (cualquiera puede loguearse, como ahora).
- Si tiene contenido y `githubUser.Login` (case-insensitive) **no** está en la
  lista ⇒ **no** crear JWT; `http.Redirect(w, r, "/?auth_error=unauthorized", …)`.
- Si está en la lista (o lista vacía) ⇒ flujo normal (redirect `/?token=…`).

Extraer el check a una función testeable, p.ej. `isLoginAllowed(login string) bool`.

## Config

En `docker-compose.yml` (repo de deploy), servicio `excalidraw`, añadir env:
```yaml
ALLOWED_CREATORS: "luisrovirosa"
```
Editar la lista = cambiar el compose + redeploy. (Logins no son secretos; el
compose vive en GitHub = fuente de verdad.)

## Flujo de datos
1. Carga la app → `enforceAccessGate()`.
2. Logueado (autorizado) → init normal, puede crear.
3. No logueado + sala/escena existente → init modo anónimo.
4. No logueado + nada válido → `/auth/login` → GitHub OAuth → callback:
   - login autorizado → `/?token=…` → vuelve logueado → puede crear.
   - login NO autorizado → `/?auth_error=unauthorized` → mensaje, sin bucle.

## Errores / casos borde
- Token expirado → `isLoggedIn()` = false → gate aplica.
- `loadFromFirebase` lanza (clave incorrecta) → tratar como "no existe" → login.
- **Bucle de redirección**: resuelto con `auth_error` (el gate lo detecta y muestra
  mensaje en vez de re-redirigir).
- No romper enlaces de escena compartida (`?id=`, `#json=`): anónimos permitidos.
- `ALLOWED_CREATORS` vacía → sin restricción (no bloquea a nadie por accidente).

## Pruebas
Backend (unit): `isLoginAllowed` — permitido / no permitido / case-insensitive /
lista vacía (=todos permitidos) / espacios alrededor de comas.

Manuales en producción tras deploy:
1. Anónimo, URL de sala existente → carga (sin redirect). ✅
2. Anónimo, app en blanco → redirige a `/auth/login`. ✅
3. Anónimo, `#room=` inexistente → redirige a login. ✅
4. Login con `luisrovirosa` → entra, puede crear. ✅
5. Login con otra cuenta de GitHub → mensaje "no autorizado", sin token, sin bucle. ✅
6. Anónimo, enlace `?id=` existente → carga (no roto). ✅

## Límites conocidos (aceptados)
- Gate a nivel de app/JS y login; esquivable fabricando peticiones a mano.
- Colaboración en vivo efímera por websocket sobre un room-id inventado, **sin**
  persistencia, sigue posible.
- Una sala ya creada la puede editar cualquiera con la URL.

## Despliegue
1. Parche de frontend (gate) + cambio de backend (callback) en `excalidraw-full`;
   push a `main` → build de imagen GHCR.
2. `ALLOWED_CREATORS` en `docker-compose.yml` del repo de deploy.
3. Redeploy del VPS (`deploy.yml`).

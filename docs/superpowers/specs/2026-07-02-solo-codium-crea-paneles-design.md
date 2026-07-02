# Diseño: "Solo Codium crea paneles" — Fase 1

Fecha: 2026-07-02
Repo: `CodiumTeam/excalidraw-full` (imagen desplegada en draw.codium.team)

## Objetivo global (contexto)
Que solo la gente de Codium pueda **crear** paneles (salas de colaboración `#room=`),
mientras cualquiera pueda **abrir/ver/editar** una sala **existente** compartida por URL.

Enfoque acordado: **gate blando** (a nivel de aplicación, no criptográficamente
inviolable). Se asume que un usuario técnico fabricando peticiones a mano podría
saltárselo; el objetivo es impedir la creación por parte de externos/curiosos por
las vías normales de la app.

Se entrega en dos fases independientes:
- **Fase 1 (este spec):** los usuarios **no logueados** solo pueden acceder a
  salas que **ya existen**. Para llegar al lienzo en blanco / crear, hay que estar
  logueado (cualquier cuenta de GitHub).
- **Fase 2 (futuro, fuera de alcance aquí):** entre los usuarios logueados, solo
  una **lista blanca** de logins de GitHub puede crear (gate del botón "Start
  session" vía claim `allowedToCreate`).

## Alcance de la Fase 1

Regla de acceso al **cargar la app**:

| Situación | Resultado |
|---|---|
| Logueado (token válido no expirado) | Acceso normal (puede crear) |
| No logueado + URL con `#room=` de una sala que **existe** en el servidor | Acceso anónimo permitido (ver/editar) |
| No logueado + enlace de escena compartida existente (`?id=` / `#json=`) | Acceso anónimo permitido (no romper links de "export to link") |
| No logueado + **sin** `#room=` (app en blanco) | Redirige a login (`/auth/login`) |
| No logueado + `#room=` de una sala que **no existe** | Redirige a login |

Con esto, un externo sin cuenta no puede llegar al lienzo en blanco ni a "Start
session", y comprobar que la sala **existe** de verdad corta también el truco de
inventarse una URL `#room=` nueva.

## Arquitectura / componentes

Solo **frontend**. Se implementa como parche de build sobre el submódulo
`BetterAndBetterII/excalidraw` (mismo mecanismo que el parche de imágenes de sala:
patch aplicado en `excalidraw-complete.Dockerfile` con `git apply` tras `rm -f .git`).

**Cero cambios de backend. Sin config nueva.** Se reutiliza:
- Estado de login: `localStorage.token` + expiración (misma lógica que `hooks/useAuth.ts`).
- Detección de sala: `getCollaborationLinkData(window.location.href)` (`data/index.ts`).
- Existencia de sala: `loadFromFirebase(roomId, roomKey)` (`data/firebase.ts`) →
  devuelve `null` si la escena no existe (usa el endpoint anónimo `batchGet` que ya existe).
- Redirect a login: `window.location.href = "/auth/login"`.

### Punto de enganche
En `excalidraw-app/App.tsx`, en el arranque de la inicialización de escena
(el effect que llama a `initializeScene`, ~línea 613), **antes** de inicializar
la escena/colaboración, ejecutar un gate asíncrono:

```
async function enforceAccessGate(): Promise<boolean> {
  if (isLoggedIn()) return true;                 // logueado → pasa (Fase 2 afinará)
  const roomLinkData = getCollaborationLinkData(window.location.href);
  const hasSharedScene = /* ?id= o #json= presente */;
  if (roomLinkData) {
    const exists = await roomSceneExists(roomLinkData); // loadFromFirebase != null
    if (exists) return true;                      // sala existente → anónimo OK
  } else if (hasSharedScene) {
    return true;                                  // escena compartida existente → OK
  }
  window.location.href = "/auth/login";           // si no, a login
  return false;                                   // corta la inicialización
}
```

- `isLoggedIn()`: token en `localStorage` válido y no expirado (misma comprobación
  que `useAuth.ts`; extraer a helper reutilizable si conviene).
- `roomSceneExists(roomLinkData)`: `(await loadFromFirebase(roomId, roomKey, null)) != null`.
  Si el enlace es válido, la clave descifra; una sala inexistente da `null`.

Si `enforceAccessGate()` devuelve `false`, no se llama a `initializeScene` (la
navegación a `/auth/login` ya se ha disparado).

## Flujo de datos
1. Carga la app → `enforceAccessGate()`.
2. Logueado → continúa init normal.
3. No logueado + sala/escena existente → continúa init (modo anónimo).
4. No logueado + nada válido → `location = /auth/login` → GitHub OAuth →
   callback `/?token=…` → vuelve logueado → puede crear.

## Manejo de errores / casos borde
- Token expirado → `isLoggedIn()` = false → gate aplica.
- `loadFromFirebase` lanza (p.ej. clave incorrecta) → tratar como "no existe" →
  redirige a login (no filtrar contenido).
- Evitar bucle de redirección: la landing tras login es `/` **logueado**, así que
  el gate pasa; nunca se redirige estando logueado.
- No romper enlaces de escena compartida (`?id=`, `#json=`): se permiten anónimos.

## Pruebas
Manuales en producción tras deploy:
1. Anónimo, URL de sala **existente** → carga la sala (sin redirect). ✅
2. Anónimo, app en blanco (sin `#room=`) → redirige a `/auth/login`. ✅
3. Anónimo, `#room=` de sala **inexistente** → redirige a login. ✅
4. Logueado, app en blanco → carga (puede crear). ✅
5. Anónimo, enlace de escena compartida `?id=` existente → carga (no roto). ✅

## Límites conocidos (aceptados)
- Gate a nivel de app/JS; esquivable fabricando peticiones a mano.
- Sigue posible colaboración en vivo efímera por websocket sobre un room-id
  inventado, **sin** persistencia.
- Una sala ya creada la puede editar cualquiera con la URL.
- La lista blanca (restringir *qué* logueados crean) es **Fase 2**, no incluida aquí.

## Despliegue
1. Añadir el parche de Fase 1 al Dockerfile (junto al de imágenes).
2. Push a `main` de `excalidraw-full` → build de imagen GHCR.
3. Redeploy del VPS (`deploy.yml`).

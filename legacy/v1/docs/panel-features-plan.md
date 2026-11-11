# GroveAuth Panel Features Plan - Modpack & World Management

> Draft plan for adding modpack management and world reset features to the GroveAuth dashboard

---

## Overview

Add new admin panel features for:
1. **Modpack Management** - Upload, replace, delete mods
2. **World Management** - Reset world, manage backups
3. **Config Management** - Edit server.properties, whitelist, ops

---

## Phase 1: Backend API Routes (mc-control worker)

### New API Endpoints

```typescript
// Mod Management
GET    /api/mc/mods              // List all mods in R2
DELETE /api/mc/mods              // Delete all mods (for fresh modpack)
POST   /api/mc/mods/upload       // Upload mod file (multipart)
DELETE /api/mc/mods/:filename    // Delete specific mod

// World Management
GET    /api/mc/world             // Get world info (size, last backup)
DELETE /api/mc/world             // Delete current world (reset)
GET    /api/mc/backups           // List available backups
POST   /api/mc/backups/:id/restore  // Restore from backup

// Config Management
GET    /api/mc/config/:file      // Get config file contents
PUT    /api/mc/config/:file      // Update config file
```

### Implementation Files

**mc-control worker (`src/worker/src/routes/`):**

```
routes/
├── mods.ts          # Mod management endpoints
├── world.ts         # World management endpoints
├── backups.ts       # Backup management
└── config.ts        # Server config management
```

### Safety Checks

All destructive operations require:
- Server must be OFFLINE (state check)
- Admin authentication
- Confirmation token (prevent accidental deletion)

```typescript
// Example: Delete all mods
async function deleteAllMods(c: Context) {
  // 1. Verify admin auth
  // 2. Check server state is OFFLINE
  if (await getServerState(c.env) !== 'OFFLINE') {
    return c.json({ error: 'Server must be offline' }, 400);
  }
  // 3. Require confirmation header
  if (c.req.header('X-Confirm-Delete') !== 'DELETE_ALL_MODS') {
    return c.json({ error: 'Confirmation required' }, 400);
  }
  // 4. Delete from R2
  const bucket = c.env.MC_ASSETS;
  const list = await bucket.list({ prefix: 'mods/' });
  for (const obj of list.objects) {
    await bucket.delete(obj.key);
  }
  return c.json({ success: true, deleted: list.objects.length });
}
```

---

## Phase 2: GroveAuth Proxy Routes

**Add to `GroveAuth/src/routes/minecraft.ts`:**

```typescript
// Mod Management (proxy to mc-control)
minecraft.get('/mods', (c) => proxyToMcControl(c, 'GET', '/api/mc/mods'));
minecraft.delete('/mods', (c) => proxyToMcControl(c, 'DELETE', '/api/mc/mods'));
minecraft.post('/mods/upload', handleModUpload);  // Special handling for multipart
minecraft.delete('/mods/:filename', (c) => proxyToMcControl(c, 'DELETE', `/api/mc/mods/${c.req.param('filename')}`));

// World Management
minecraft.get('/world', (c) => proxyToMcControl(c, 'GET', '/api/mc/world'));
minecraft.delete('/world', (c) => proxyToMcControl(c, 'DELETE', '/api/mc/world'));
minecraft.get('/backups', (c) => proxyToMcControl(c, 'GET', '/api/mc/backups'));
minecraft.post('/backups/:id/restore', (c) => proxyToMcControl(c, 'POST', `/api/mc/backups/${c.req.param('id')}/restore`));
```

---

## Phase 3: Frontend Dashboard Components

### New Dashboard Sections

**Location:** `GroveAuth/frontend/src/routes/dashboard/minecraft/+page.svelte`

```
┌─────────────────────────────────────────────────────────────┐
│  Minecraft Server Admin                                      │
├─────────────────────────────────────────────────────────────┤
│  [Status] [Players] [Auto-Shutdown] [Session Cost]          │
├─────────────────────────────────────────────────────────────┤
│  [World Size] [Last Backup] [Uptime]                        │
├───────────────────────────┬─────────────────────────────────┤
│  Whitelist                │  Console                        │
├───────────────────────────┴─────────────────────────────────┤
│  📦 MODPACK MANAGEMENT (NEW)                                │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ Current: 188 mods (245 MB)              [Delete All] ↻  ││
│  │ ┌──────────────────────────────────────────────────────┐││
│  │ │ mod-name.jar                           12 MB  [×]   │││
│  │ │ another-mod.jar                         5 MB  [×]   │││
│  │ │ ...                                                  │││
│  │ └──────────────────────────────────────────────────────┘││
│  │                                                         ││
│  │ Upload: [Choose Files...] or drag & drop               ││
│  │         □ Auto-filter client-only mods                  ││
│  └─────────────────────────────────────────────────────────┘│
├─────────────────────────────────────────────────────────────┤
│  🌍 WORLD MANAGEMENT (NEW)                                  │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ Current World: 2.9 GB                                   ││
│  │ Seed: 8586235716160945827                               ││
│  │ Last Backup: Dec 12, 3:45 PM                            ││
│  │                                                         ││
│  │ [Reset World]  [Download Backup]                        ││
│  │                                                         ││
│  │ Recent Backups:                                         ││
│  │ • 2024-12-12_15-45.tar.gz (2.9 GB) [Restore] [Download] ││
│  │ • 2024-12-11_22-30.tar.gz (2.8 GB) [Restore] [Download] ││
│  └─────────────────────────────────────────────────────────┘│
├─────────────────────────────────────────────────────────────┤
│  Session History                                            │
└─────────────────────────────────────────────────────────────┘
```

### Component Structure

```
frontend/src/routes/dashboard/minecraft/
├── +page.svelte           # Main dashboard (existing)
├── +page.ts               # Data loader (existing)
└── components/
    ├── ModpackManager.svelte    # NEW: Mod list & upload
    ├── WorldManager.svelte      # NEW: World reset & backups
    └── ConfirmDialog.svelte     # NEW: Confirmation modal
```

### UI Components

**ModpackManager.svelte:**
```svelte
<script lang="ts">
  let mods = $state<ModInfo[]>([]);
  let isLoading = $state(false);
  let uploadProgress = $state(0);

  async function deleteAllMods() {
    if (!confirm('Delete ALL mods? Server must be offline.')) return;
    // Double confirmation with typed input
    const input = prompt('Type "DELETE ALL MODS" to confirm:');
    if (input !== 'DELETE ALL MODS') return;
    // Call API
  }

  async function uploadMods(files: FileList) {
    // Filter client-only if checkbox enabled
    // Upload each file with progress
  }
</script>
```

**WorldManager.svelte:**
```svelte
<script lang="ts">
  async function resetWorld() {
    if (!confirm('Reset world? This cannot be undone.')) return;
    const seed = prompt('Enter seed (or leave blank for random):');
    // Call API with optional seed
  }

  async function restoreBackup(backupId: string) {
    if (!confirm('Restore this backup? Current world will be replaced.')) return;
    // Call API
  }
</script>
```

---

## Phase 4: Implementation Order

### Sprint 1: Backend API (mc-control)
1. [ ] Add mod listing endpoint (`GET /api/mc/mods`)
2. [ ] Add mod deletion endpoint (`DELETE /api/mc/mods`)
3. [ ] Add individual mod delete (`DELETE /api/mc/mods/:filename`)
4. [ ] Add world info endpoint (`GET /api/mc/world`)
5. [ ] Add world reset endpoint (`DELETE /api/mc/world`)
6. [ ] Add backup listing (`GET /api/mc/backups`)

### Sprint 2: GroveAuth Backend
1. [ ] Add proxy routes for all new mc-control endpoints
2. [ ] Add multipart upload handling for mod files
3. [ ] Test all endpoints with curl/Postman

### Sprint 3: Frontend UI
1. [ ] Create ModpackManager component
2. [ ] Create WorldManager component
3. [ ] Create ConfirmDialog component
4. [ ] Integrate into main dashboard page
5. [ ] Add drag-and-drop file upload
6. [ ] Add progress indicators

### Sprint 4: Polish & Safety
1. [ ] Add server-offline check to all destructive operations
2. [ ] Add audit logging for admin actions
3. [ ] Add client-only mod filter list to frontend
4. [ ] Test all workflows end-to-end

---

## Security Considerations

1. **Authentication**: All endpoints require valid admin JWT
2. **State Check**: Destructive ops only when server OFFLINE
3. **Rate Limiting**: Prevent rapid mod uploads (DoS)
4. **File Validation**:
   - Only .jar files for mods
   - Max file size (50MB per mod?)
   - Virus scanning? (future)
5. **Audit Trail**: Log all admin actions to D1

---

## Data Structures

```typescript
interface ModInfo {
  filename: string;
  size: number;        // bytes
  uploaded: string;    // ISO date
  checksum?: string;   // SHA256
}

interface WorldInfo {
  size: number;        // bytes
  seed?: string;
  lastBackup?: string; // ISO date
  backupCount: number;
}

interface BackupInfo {
  id: string;          // timestamp-based
  created: string;     // ISO date
  size: number;        // bytes
  downloadUrl?: string; // Signed URL for download
}
```

---

## CLI vs Panel Feature Parity

| Feature | CLI (r2-manage.sh) | Panel (Future) |
|---------|-------------------|----------------|
| List mods | ✅ | 🔜 |
| Delete all mods | ✅ | 🔜 |
| Upload mods | ✅ | 🔜 |
| Delete single mod | ❌ | 🔜 |
| List world | ✅ | 🔜 |
| Reset world | ✅ | 🔜 |
| List backups | ✅ | 🔜 |
| Restore backup | ❌ | 🔜 |
| Download backup | ❌ | 🔜 |
| Custom seed | ❌ | 🔜 |

---

## Estimated Effort

- **Backend API (mc-control)**: 4-6 hours
- **GroveAuth proxy routes**: 1-2 hours
- **Frontend components**: 6-8 hours
- **Testing & polish**: 2-4 hours

**Total**: ~15-20 hours

---

*Created: 2025-12-15*
*Status: Draft*

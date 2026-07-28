// @ts-check
// Saving a backup — the one thing nobody else can do for you.
//
// This lives in "me" rather than in settings because it is about your
// identity, and because a feature people have to go looking for is a feature
// people take once and never again. The node records THAT a backup happened
// and never where it went, so the nudge below can be honest without the app
// knowing where your keys are kept.

/** How stale a backup has to get before the nudge stops being polite. */
const BACKUP_NAG_DAYS = 30;

async function refreshBackupState() {
  const el = document.getElementById('backupState');
  if (!el) return;
  try {
    const s = await api('/api/backup/state');
    if (!s.ever) {
      el.textContent = 'You have never saved a backup from this device. ' +
        'If this machine is lost, nothing here can be recovered — there is no ' +
        'server holding a copy.';
      el.className = 'hint warn';
      return;
    }
    const days = s.age_days || 0;
    el.className = days >= BACKUP_NAG_DAYS ? 'hint warn' : 'hint';
    el.textContent = days === 0
      ? 'Last backup: today.'
      : `Last backup: ${days} ${days === 1 ? 'day' : 'days'} ago.`;
  } catch { el.textContent = ''; }
}

async function saveBackup() {
  const pass = /** @type {HTMLInputElement} */ (document.getElementById('backupPass')).value;
  if (!pass) { alert('Choose a passphrase for the backup file.'); return; }
  if (pass.length < 8) { alert('The backup passphrase needs at least 8 characters.'); return; }

  const btn = document.getElementById('backupBtn');
  const was = btn.textContent;
  btn.disabled = true;
  btn.textContent = 'Packing…';
  try {
    // Not through api(): this response is a file, not JSON, and it can be the
    // size of the whole history.
    const token = new URLSearchParams(location.search).get('token');
    const res = await fetch('/api/backup', {
      method: 'POST',
      headers: { 'X-QP-Token': token, 'Content-Type': 'application/json' },
      body: JSON.stringify({ passphrase: pass }),
    });
    if (!res.ok) {
      let msg = 'The backup failed.';
      try { msg = (await res.json()).error || msg; } catch { /* not JSON */ }
      throw new Error(msg);
    }
    const blob = await res.blob();
    const name = (res.headers.get('Content-Disposition') || '')
      .match(/filename="([^"]+)"/)?.[1] || 'quiet-spaces.qsbackup';

    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = name;
    document.body.appendChild(a); a.click(); a.remove();
    URL.revokeObjectURL(url);

    // Clear the passphrase from the field the moment it has been used.
    /** @type {HTMLInputElement} */ (document.getElementById('backupPass')).value = '';
    refreshBackupState();
    alert('Backup saved as ' + name + '.\n\n' +
      'Keep it somewhere this machine cannot take with it. Nobody — including ' +
      'us — can open it without that passphrase, and nobody can reset it.');
  } catch (err) {
    alert(err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = was;
  }
}

if (typeof window !== 'undefined') {
  Object.assign(window, { saveBackup, refreshBackupState });
}

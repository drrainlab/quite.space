// i18n foundation: every user-facing string flows through t(key, vars).
// Plural + interpolation + Intl relative time; no string concatenation of
// independent keys (so the DOM never bakes in English sentence structure).
// EN is the source locale; a RU catalog drops in later without touching code.

const I18N = {
  en: {
    // shell / connection
    'app.name': 'quiet spaces',
    'conn.direct': 'Direct',
    'conn.lan': 'Local network',
    'conn.mesh': 'Mesh',
    'conn.relay': 'Through a relay',
    'conn.syncing': 'Catching up',
    'conn.offline': 'No connection right now',
    'conn.off': 'Not connected',
    'conn.here': ({ count }) => `${count} ${plural(count, 'person', 'people')} here`,
    'conn.summary': ({ state, count }) => `${state} · ${count} here`,
    'conn.details': 'Connection details',
    'conn.mesh_node': ({ node }) => `mesh · node ${node}`,

    // spaces list
    'spaces.new': '+ space',
    'spaces.join': 'join',
    'spaces.me': 'me',
    'spaces.owner': 'owner',
    'spaces.empty.title': "It's quiet here so far.",
    'spaces.empty.body': 'Create your own space, or enter with a pass.',
    'spaces.activity.events': ({ count }) => `${count} ${plural(count, 'moment', 'moments')}`,
    'spaces.unread': ({ count }) => `${count} new`,

    // Navigator (NAV-1). Sections a person arranged, not a list we sorted.
    'nav.search': 'Search',
    'nav.sec.pinned': 'Pinned',
    'nav.sec.groups': 'Groups',
    'nav.sec.spaces': 'Spaces',
    'nav.sec.people': 'People',
    'nav.sec.catalogs': 'Catalogs',
    'nav.more': 'more',
    'nav.pin': 'Pin',
    'nav.unpin': 'Unpin',
    'nav.add_to': ({ title }) => `Add to ${title}`,
    'nav.remove_from_group': 'Remove from this group',
    'nav.remove': 'remove',
    'nav.move_up': 'Move up',
    'nav.move_down': 'Move down',
    'nav.move_top': 'Move to top',
    'nav.group.new': 'New group…',
    'nav.group.name': 'Name this group',
    'nav.group.rename': 'Rename…',
    'nav.group.delete': 'Delete group',
    // Say what is actually destroyed. A group is a list of links, so
    // deleting one loses the list and nothing that was in it.
    'nav.group.delete_confirm': ({ title }) =>
      `Delete the group "${title}"? The spaces in it stay exactly where they are.`,
    'nav.gone': 'not in this space any more',
    'nav.pinned.empty': 'Pin what you come back to.',
    'nav.groups.empty': 'Make a group to gather what belongs together.',
    // Not "no people": say the rule, so the section is not a mystery.
    'nav.people.empty': 'Somebody appears here when you share a space with exactly one other person.',
    'nav.catalogs.empty': 'Catalogs you open will be listed here.',
    // The boundary, stated where somebody hits it rather than in docs.
    'nav.search.none': 'Nothing by that name. Navigator searches names, not what was said inside.',

    // The personal assistant (AI-0). Every one of these says where the
    // words go, because that is the fact a person needs before typing.
    'ai.window.remote': ({ provider, model }) =>
      `Quite AI sees the last 20 messages of this space and nothing else. ` +
      `Every question re-sends that window to ${provider} · ${model} over the internet.`,
    'ai.window.local': ({ model }) =>
      `Quite AI sees the last 20 messages of this space and nothing else. ` +
      `Every question goes to ${model} on this machine. Nothing leaves the device.`,
    'ai.unconfigured': 'Set up an AI provider in Settings — this space has nowhere to send a question yet.',
    'ai.thinking': 'Asked. Waiting for the provider…',
    'ai.failed': ({ why }) => `No answer: ${why}. Ask again when you like.`,

    // conversation
    'conv.member': 'member',
    'conv.you': 'You',
    'conv.write': 'write into the space…',
    'conv.send': 'send',
    'conv.save_card': 'Save as card',
    'conv.reply': 'reply',
    'conv.reply_hint': 'answer this message and address its author',
    'conv.replying_to': ({ name }) => `replying to ${name}`,
    'conv.cancel_reply': 'cancel reply',
    'conv.edited': 'edited',
    'entry.unsupported': 'unsupported content',

    // system events (human)
    'sys.joined_with_pass': ({ who, owner }) => `${who} entered with ${owner}'s pass`,
    'sys.joined': ({ who }) => `${who} joined`,
    'sys.connection_changed': 'Connection changed',
    'sys.connection_changed_body': 'Messages will continue over the local network.',

    // honesty
    'honesty.undecryptable': ({ count }) =>
      `${count} ${plural(count, 'moment', 'moments')} you can't read here yet (no key — shown, not hidden)`,
    'honesty.simulated': 'SIMULATED',

    // presence
    'presence.here': 'here',
    'presence.away': 'away briefly',
    'presence.listening': 'listening',
    'presence.recording': 'recording',
    'presence.unknown': 'presence unknown',
    'presence.last_seen': ({ who, ago }) => `${who} was here ${ago}`,
    'presence.is_here': ({ who }) => `${who} is here`,
    'presence.pick': 'presence…',

    // device
    'device.title': 'This device',
    'device.trusted': 'Trusted device',
    'device.verification': 'Verification code',
    'device.add': 'Add another device',
    'device.security': 'Security',
    'device.technical': 'Technical details',

    // protocol view
    'protocol.toggle': 'Protocol view',
    'protocol.on': 'Protocol view is on',
    'protocol.off': 'Protocol view is off',

    // space pass (UI-2)
    'pass.title': 'Space Pass',
    'pass.invite_from': ({ who }) => `an invitation from ${who}`,
    'pass.card_hint': 'A pass lets someone ask to enter. Your device confirms it — nobody gets in until then.',
    'pass.verify_hint': 'Read this phrase to the person you\'re inviting, so they know the pass is really from you:',
    'pass.terms_one': ({ hours }) => `1 entry · valid ${hours}h`,
    'pass.terms_many': ({ uses, hours }) => `${uses} entries · valid ${hours}h`,
    'pass.relay': 'Rendezvous relay (where the request waits):',
    'pass.relay_ph': 'relay address, e.g. 192.168.1.10:7411',
    'pass.configure': 'Configure',
    'pass.uses': 'Who can enter',
    'pass.uses_1': 'one person',
    'pass.uses_10': 'up to 10',
    'pass.ttl': 'Valid for',
    'pass.ttl_1': '1 hour',
    'pass.ttl_24': '24 hours',
    'pass.ttl_168': '7 days',
    'pass.create': 'Create a pass',
    'pass.copy': 'Copy pass',
    'pass.copied': 'Copied',
    'pass.show_qr': 'Show QR',
    'pass.revoke': 'Revoke',
    'pass.revoked': 'This pass has been revoked. People already inside stay — revoking only stops new entries.',
    'pass.tech_invite': 'Invite a device directly (technical)',
    'pass.need_relay': 'Set a relay in Settings first — that\'s where the entry request waits.',

    // join by pass (async state machine)
    // A space nobody named says who is in it. These are projections, not
    // titles: the node sends a key and the people, so the sentence is
    // built in the reader's own language.
    'space.waiting': 'waiting for someone',
    'space.someone_arrived': 'someone arrived',
    'space.with_one': ({ names }) => names[0],
    'space.with_many': ({ names, more }) =>
      more > 0 ? `${names.join(', ')} and ${more} more`
               : names.slice(0, -1).join(', ') + ' and ' + names[names.length - 1],
    'space.local_name_note': 'This name stays on this device.',
    'join.title': 'Enter with a pass',
    'join.paste': 'Paste the pass link someone shared with you:',
    'join.enter': 'Request entry',
    'join.waiting': 'Request sent — waiting for the owner\'s device to confirm your entry. You can close this window.',
    'join.offline': 'The owner\'s device is offline. Your request waits and will be delivered when one of you reconnects.',
    'join.ready': 'Entry confirmed — the space is ready.',
    'join.open': 'Open the space',
    'join.expired': 'This pass has expired. Ask for a new one.',
    'join.expired_waiting': 'The pass expired before the owner\'s device could confirm. Ask for a new pass.',
    'join.revoked': 'This pass was revoked before your entry was confirmed. Ask for a new one.',
    'join.rejected': 'That entry could not be completed. Ask for a new pass.',

    // generic
    'action.close': 'close',
    'action.cancel': 'cancel',
    'action.share': 'Share',
    'action.copy': 'Copy link',
    'action.open': 'Open',
    'action.retry': 'Retry',
    'time.now': 'just now',
  },
};

let LOCALE = 'en';

function plural(n, one, many) { return n === 1 ? one : many; }

// t(key, vars) — resolves against the active locale, falls back to the key
// itself in dev so a missing string is loud, not silent.
function t(key, vars) {
  const cat = I18N[LOCALE] || I18N.en;
  let entry = cat[key];
  if (entry === undefined) entry = I18N.en[key];
  if (entry === undefined) { console.warn('i18n: missing key', key); return key; }
  return typeof entry === 'function' ? entry(vars || {}) : entry;
}

// Relative time via Intl — never hand-rolled "N minutes ago" strings.
const RTF = new Intl.RelativeTimeFormat(LOCALE === 'en' ? 'en' : LOCALE, { numeric: 'auto' });
function relTime(seconds) {
  if (seconds < 45) return t('time.now');
  if (seconds < 5400) return RTF.format(-Math.round(seconds / 60), 'minute');
  if (seconds < 86400) return RTF.format(-Math.round(seconds / 3600), 'hour');
  return RTF.format(-Math.round(seconds / 86400), 'day');
}

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
    // "Radio", not "Mesh": the ordinary chip names the physical form the
    // connection took, never the vendor whose firmware happens to be on the
    // board. A second carrier arrived and the word had to stop being one
    // driver's — the same reason ModeMeshtasticOnly became ModeRadioOnly.
    'conn.radio': 'Radio',
    'conn.relay': 'Through a relay',
    'conn.syncing': 'Catching up',
    'conn.offline': 'No connection right now',
    'conn.off': 'Not connected',
    'conn.here': ({ count }) => `${count} ${plural(count, 'person', 'people')} here`,
    'conn.summary': ({ state, count }) => `${state} · ${count} here`,
    'conn.details': 'Connection details',
    'conn.mesh_node': ({ node }) => `mesh · node ${node}`,
    // For a carrier that has no node numbers, which is every carrier except
    // Meshtastic. Naming the driver here is a DIAGNOSTIC, not a choice
    // offered to anybody — the person never picked it and cannot change it
    // from this chip.
    'conn.radio_carrier': ({ carrier }) => `radio · ${carrier}`,

    // Why something of yours has not gone out yet. Never "failed": it is
    // waiting for a path wide enough, and it will go when one appears.
    'entry.waiting.wider_path': ({ size, fits }) =>
      `Waiting for a wider path — ${size}, and the radio carries ${fits}. ` +
      `It goes as soon as the internet or the local network is back. Nothing is lost.`,

    // Creating a place, described by what it is FOR (CAT-0b). The words
    // this preset exists to keep off the screen are "broadcast", "curated"
    // and "kind" — a person creating a list of places should meet none of
    // them.
    'wiz.tpl.directory': '\u{1F4D6} Directory',
    'wiz.tpl.directory_desc': 'a place that lists other places',
    'wiz.remember': '4 \u00b7 what should it remember?',
    'wiz.name_ph': 'name of the place',
    'wiz.dir.title': 'Name your directory',
    'wiz.dir.name_ph': 'name of the directory',
    // Broadcast, said as its consequence rather than as its name.
    'wiz.dir.note': 'Anyone with the link can read this directory, and only ' +
      'you add entries to it. The link cannot be taken back.',

    // The Access sheet. One policy value, two vocabularies: an ordinary
    // space is asked who may WRITE, a directory who may ADD ENTRIES. The
    // words "broadcast", "curated" and "kind" appear in neither.
    'access.who_writes_label': 'Who can write',
    'access.writes_anyone': 'Anyone who comes in',
    'access.writes_me': 'Only me and curators',
    'access.who_adds_label': 'Who can add entries',
    'access.adds_anyone': 'Anyone who can post',
    'access.adds_me': 'Only me and curators',
    'access.curated_confirm': 'From now on only you and the curators can ' +
      'post here. Everything already written stays exactly where it is.',
    'access.directory_needs_public': 'A directory is a list other people ' +
      'read, so this space has to be reachable by a link first.',
    'access.directory_hint': 'People find this place by its link, and what ' +
      'they see is the list of places you put in it.',

    // attaching a radio from the interface
    'radio.attach.ask': 'The phrase your segment shares.\n\nEvery radio on a segment derives the same key from these exact words, so it has to be identical everywhere — including on a node started with --mesh-seed. At least 16 characters.',
    'radio.detach': 'detach this radio',
    'radio.detach.confirm': 'Put this radio down and forget it? Nothing is lost — messages keep moving over the internet and the local network, and you can attach it again whenever you like.',

    // meeting over the radio (MR-1) — no relay, no internet
    'radio.meet.intro': 'Two radios and nobody else. Saying who you are puts this device\u2019s name and public key on the air, where everyone in range hears it \u2014 that is what somebody needs in order to seal an invitation to you. Nothing is joined until you answer it.',
    // The two presses of the radio introduction, named for what each DOES.
    // One frame to find out whether they can hear you; six to hand over a way
    // in. Calling both "start a line" made the first look like a failure.
    'radio.meet.establish': 'check they can hear you',
    'radio.meet.checking': 'checking\u2026',
    'radio.meet.working': 'going out\u2026',
    'radio.meet.reaching': 'Reaching out over the radio\u2026',
    'radio.meet.probe_sent': 'Asked whether they can hear you \u2014 one frame, so nothing was spent. The answer appears on their row; press again when it says the link is up.',
    'radio.meet.offer_again': 'offer again',
    'radio.meet.link_unknown': 'Not asked yet whether they can hear you.',
    // What the people did, which outranks anything the carrier saw.
    'radio.meet.granting': 'They accepted. The way in is going out over the air \u2014 this takes a minute on LoRa.',
    'radio.meet.line_ready': 'The line is here.',
    'radio.meet.open_line': 'open the line',
    'radio.meet.arrived_title': 'the line is here',
    'radio.meet.arrived': ({ who }) => `${who} granted it, and the space has arrived on this device. Nothing else is needed.`,

    'radio.meet.offers': 'waiting for your answer',
    'radio.meet.offers_hint': 'Sealed to this device, so nobody else in range could open it. Accepting is what joins you \u2014 until then it has cost you nothing.',
    'radio.meet.offer': ({ from, space }) => `${from} offers a way into ${space}`,
    'radio.meet.accept': 'accept',
    'radio.meet.heard': 'heard on this segment',
    'radio.meet.heard_hint': 'Everything here is what a radio claimed about itself. An invitation is sealed to the key it announced, so somebody claiming another name receives an invitation they cannot open.',
    'radio.meet.nobody': 'Nobody has announced themselves yet. They press \u201csay who I am\u201d on their side, and LoRa is slow \u2014 give it a few seconds.',
    'radio.meet.unnamed': 'a radio with no name',
    'radio.meet.invite_into': ({ space }) => `invite into \u201c${space}\u201d`,
    'radio.meet.invite_here': 'invite into this space',
    'radio.meet.start_line': ({ who }) => `start a line with ${who}`,
    'radio.meet.also_into': ({ space }) => `or invite them into \u201c${space}\u201d`,
    'radio.meet.also_here': 'or invite them into the space that is open',
    'radio.meet.public_space': 'This space is public, so there is nothing to seal to one device \u2014 anyone with its address can read it. Open an ordinary space to invite somebody over the radio.',
    'radio.meet.offering_short': 'going out\u2026',
    'radio.meet.open_a_space': 'Open the space you want to invite them into first.',
    'radio.meet.announcing': 'Going out over the radio. LoRa is slow \u2014 give it half a minute, and watch \u201con the air\u201d below for movement. Everyone within range will know this device is here.',
    'radio.meet.air': 'on the air',
    'radio.meet.messages': 'messages',
    'radio.meet.frames': 'frames',
    'radio.meet.in_flight': ({ done, all }) => `${done} of ${all} across, one still going`,
    'radio.meet.idle': ({ done }) => `${done} across, nothing in flight`,
    'radio.meet.gave_up': ({ n }) => `${n} did not get through \u2014 the other radio stopped answering.`,
    'radio.meet.no_radio': 'No radio is connected, so nothing can go out.',
    'radio.meet.no_seed': 'This radio is connected without a segment seed, so it cannot carry an introduction. Restart with --mesh-seed.',
    'radio.meet.offering': 'Offered over the radio. Nobody is a member and no key has moved until they answer \u2014 watch \u201con the air\u201d below.',
    'radio.meet.joined': 'Joined.',
    'radio.meet.unheard': 'The send over the radio could not begin. Try again when the radio is connected.',
    'radio.meet.unconfirmed': 'Could not confirm delivery. The invitation may already have arrived \u2014 a repeat will not create a second line.',
    'radio.meet.delivered': 'They heard it. Waiting for them to answer.',
    'radio.meet.link_up': 'Radio link established.',
    'radio.meet.link_direct': 'Direct radio link.',
    'radio.meet.link_probing': 'Asking whether they can hear you\u2026',
    'radio.meet.link_none': 'Nobody answered. Their radio may be off or out of range.',
    'radio.meet.link_gone': 'They were here and the radio went quiet.',
    'radio.meet.member_unaware': 'They asked to come in, and the radio went quiet before the answer reached them. They ARE a member here; their device does not know it yet. Send it again when they are back.',
    'radio.meet.answered': 'Your answer is on the air. The space appears here when they grant it \u2014 not before.',
    'radio.meet.failed': ({ why }) => `Did not work: ${why}`,
    'radio.meet.unavailable': ({ why }) => `Cannot read the segment: ${why}`,

    // transport policy. Each line says what is USED and what is not, because
    // the difference between "prefers" and "never opens" is the whole reason
    // somebody reaches for this switch.
    'conn.mode.auto': 'Whatever is available: a relay, the local network, the radio.',
    'conn.mode.internet': 'A relay and the local network. The radio stays off the air.',
    'conn.mode.radio': 'The radio only. No relay connection is opened at all.',
    'conn.mode.offline': 'Nothing leaves this device. What you write is kept and waits.',
    // What the CHIP says when the policy, not the network, is the reason.
    // "Not connected" would send somebody looking for a fault they do not
    // have; both of these name the choice that is in force.
    'conn.policy.offline': 'Offline — by your setting',
    'conn.policy.unreadable': 'Held — transport setting unreadable',
    'conn.mode.unreadable': ({ mode }) =>
      `This device has "${mode}" stored, which this build does not understand — so nothing is sent until you choose one above.`,

    // relay topology (RR wave)
    'relay.mode.auto':'Relays are chosen by measuring the real path from this device. The best one is used, a second is kept as a backup.',
    'relay.mode.custom': 'Only the relay you name below is used — never an official one behind your back.',
    'relay.saved.auto': 'saved — measuring relays…',
    'relay.saved.custom': 'saved — syncing through',
    'relay.off': 'relay sync off',
    'relay.measuring': 'measuring…',
    'relay.measured': 'chosen',
    'relay.none': 'nothing reachable right now',
    'relay.unreachable': 'could not reach that relay:',
    'relay.copied': 'diagnostics copied',
    'relay.diag.mode': 'Mode',
    'relay.diag.primary': 'Primary',
    'relay.diag.backup': 'Backup',
    'relay.diag.trust': 'Trust',
    'relay.diag.health': 'Health',
    'relay.diag.rtt': 'Round trip',
    'relay.diag.load': 'Relay load',
    'relay.diag.sync': 'Background sync',
    'relay.diag.on': 'running',
    'relay.diag.off': 'off',
    'relay.diag.error': 'Last error',

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
    'share.card.read': 'Read post \u2192',
    'share.card.read_hint': 'Opens a temporary look at a public space you are not in, at an address this person chose.',
    'share.card.forward': '\u2192 Forward',
    'share.card.claim': ({ sender }) => `${sender} shared this post`,
    'share.ref.toggle': 'attach a way back to the post',
    'prev.unnamed_space': 'a public space',
    'prev.untitled': 'untitled',
    'prev.waiting': 'This post is not available from the shared address right now.',
    'prev.missing': 'The space answered, but this post is not in it yet.',
    'prev.archived': 'This post was withdrawn by its author.',
    'prev.retry': 'Retry',
    'prev.back': '\u2190 Back',
    'prev.follow': 'Follow space',
    'prev.follow_ro': 'Follow read-only',
    'prev.join': 'Join and participate',
    'prev.media_unavailable': 'media is not loaded in a preview \u2014 follow the space to see it',
    'prev.load_audio': '\u25b6 Load audio',
    'prev.load_video': '\u25b6 Load video',
    // "Holder" is this protocol's word, not a reader's: somebody looking at a
    // picture that has not arrived wants to know it is LOADING first, and why
    // it is taking a moment second.
    'prev.looking': 'Loading media \u2014 looking for a peer who has it\u2026',
    'prev.progress': ({ got, total }) => `Loading media\u2026 ${got} of ${total} chunks`,
    'prev.loading': 'Loading\u2026',
    'prev.download_file': 'Download file',
    'prev.open_file': 'Open file',
    'prev.no_response': 'No holder answered this request yet.',
    'prev.descriptor': 'This publication does not currently carry enough information to request this media.',
    'prev.budget': 'This media is larger than the temporary preview limit.',
    'prev.integrity': 'The received media did not match its content hash and was not shown.',
    'prev.cancelled': 'Loading stopped when the preview was closed.',
    'prev.block_skipped': ({ kind }) => `a ${kind} block \u2014 open the space to see it`,
    'nav.search.clear': 'clear the search',
    'nav.sec.pinned': 'Pinned',
    'nav.sec.groups': 'Groups',
    'nav.sec.spaces': 'Spaces',
    'nav.sec.people': 'People',
    'nav.sec.catalogs': 'Directories',
    'nav.sec.recent': 'Recent',
    'nav.sec.ai': 'AI',
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
    'nav.catalogs.empty': 'Places that list other places show up here once you keep one.',
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

    // Catalogs (CAT-0a). A catalog is a public space of space-cards, and
    // every uncertainty about an entry is stated rather than guessed.
    // Discover, said in places rather than in mechanism (CAT-0b).
    'cat.home': 'Discover',
    'cat.loading': 'Looking\u2026',
    'cat.looking': 'Looking\u2026',
    'cat.empty': 'Nothing here yet.',
    'cat.untitled': 'untitled',
    // Never "relay unavailable", never "waiting", never "source failed" —
    // those describe the machinery, and all a person can be told honestly
    // is that nothing came back.
    'cat.silent': 'Nothing came back from there.',
    'cat.featured_silent': 'Couldn\u2019t reach the featured directory.',
    'cat.try_again': 'Try again',
    'cat.yours_still_here': 'The directories you added are still available under \u22ef',
    'cat.home_empty': 'Nothing has been added here yet.',
    'cat.add_directory': 'Add a directory',
    'cat.not_a_directory': 'That link leads to an ordinary place, not to a list of places.',
    // The act and its undo carry ONE name. A thing called "Add to Discover"
    // on one screen and "remove source" on another is how a person learns
    // the app has a second, truer name for things.
    'cat.add_discover': 'Add to Discover',
    'cat.added_discover': 'Added to Discover \u2713',
    'cat.remove_discover': 'Remove from Discover',
    'cat.official': 'Featured',
    'cat.on': 'On',
    'cat.off': 'Off',
    'cat.copy_link': 'Copy link',
    'cat.copied': 'Copied',
    'cat.leaf.frozen': 'no longer publishing',
    'cat.leaf.owner_posts': 'its owner posts here',
    'cat.leaf.anyone_posts': 'anyone who comes in can write',
    'cat.leaf.nothing': 'Nothing has been posted here yet.',
    // Named for the RESULT, and it is the first moment anything is kept.
    'cat.leaf.keep': 'Add to my spaces',
    'cat.leaf.go': 'Go there',
    'cat.cycle': ({ title }) =>
      `You are already inside \u201c${title}\u201d \u2014 this way round leads back to where you are.`,
    // Adding an entry. Unreachable is NOT a refusal: a directory that can
    // only list places which happen to be online is one that empties
    // itself during an outage.
    'cat.add.found_directory': ({ title }) =>
      title ? `Found \u201c${title}\u201d \u2014 a place that lists other places.`
            : 'Found a place that lists other places.',
    'cat.add.found_space': ({ title }) =>
      title ? `Found \u201c${title}\u201d.` : 'Found it.',
    'cat.add.unreachable': 'Nothing came back from there just now. You can still ' +
      'add it \u2014 it will show up properly once it is reachable.',
    'cat.add.itself': 'That is this place. A list cannot contain itself.',
    'cat.add.already': 'That one is already on this list.',
    'cat.add.needs_name': 'Give it a name first \u2014 that is what people will see.',
    'cat.paste': 'Add a directory\n\nPaste the link somebody gave you. A directory is an ' +
      'ordinary public place whose posts are other places.',

    'share.search': 'Search people, spaces and AI',
    'share.sec.recent': 'Recent',
    'share.sec.ai': 'AI',
    'share.send': 'Send',
    'share.send_to': ({ count }) => `Send to ${count}`,
    'share.comment': 'Add a comment…',
    'share.comment.ai': 'What should the AI do with this?',
    'share.picked': ({ names }) => `Selected: ${names}`,
    'share.about': ({ space }) => `From ${space}`,
    // Said where the choice is made rather than as a refusal afterwards.
    'share.closed': 'you cannot write here',
    'share.recent.empty': 'Places you send to will show up here.',
    'share.ai.empty': 'Set up an AI provider in Settings to send things to it.',

    // Forwarding, seen (SHARE-1). The claim line is the sentence the whole
    // wave turns on and it is never softened.
    'conv.forward': 'forward',
    'share.quote.anon': 'somebody wrote',
    'share.quote.claim': ({ sender, author }) =>
      `${sender} says this came from ${author}. Their signature did not travel — this is ${sender}'s word for it.`,
    'share.quote.claim_anon': ({ sender }) =>
      `${sender} says this came from somewhere else. Nothing here proves who wrote it.`,
    'share.result.ok': ({ name }) => `Sent to ${name}`,
    'share.result.no': ({ name, why }) => `Not sent to ${name} — ${why}`,
    // Delivery, in the ledger's own honest vocabulary. Nothing here ever
    // says "delivered": a transport can prove at most that a relay ACCEPTED
    // something, and an untracked id gets no line at all.
    'share.wait.connectivity': 'waiting — nothing is switched on to carry it',
    'share.wait.faster_link': 'waiting — this needs a bigger link than the one that is on',
    'share.wait.custody': 'a gateway is holding it',
    'share.proof.relay': 'accepted by a relay — that is not delivery',
    'share.state.settled': 'handed on',
    // "carrying" implied motion the ledger has not seen. State is pending
    // with a route CHOSEN, which is a queue — say that and nothing more.
    'share.state.queued': ({ route }) => `queued for ${route}`,

    'pane.close': 'Close',
    'space.customize': 'Appearance',
    'space.palette': 'Reaction palette',

    // what a phone's notifications may say (AR-1b.7b). Each line describes
    // what the level PUBLISHES — to a lock screen and to every notification
    // listener on the device — rather than how friendly it is, because that
    // is the fact somebody is actually choosing between.
    'notif.hidden': 'A notification says only that something arrived. No space, no sender, no text.',
    'notif.space': 'The space may be named. The sender and the message stay out of it.',
    'notif.preview': 'The space, who wrote, and the message itself — in the shade and on the lock screen.',
    'notif.saved': 'Saved on this device.',
    'notif.failed': 'Could not save that here.',

    // the "stay connected" mode (AR-1c). It costs battery and Android makes
    // an app say so; these say it in the interface too, rather than leaving
    // the system's own notice to explain a choice nobody was told about.
    'stay.on': 'On. Android will show a permanent notice while it lasts.',
    // Turning it OFF now has a consequence for other people, so switching it
    // off is where that is said — once, plainly, at the moment it becomes true.
    'stay.off': 'Off. Messages arrive while Quiet is running, and photos you '
      + 'sent can only be opened while it is.',
    'stay.failed': 'This device cannot hold the mode.',
    // Not "failed": nothing broke, and the sentence has to survive being read
    // by somebody who did not press anything. It names who switched it off
    // and leaves trying again to them.
    'stay.refused': 'Android would not let this run, so it is off. '
      + 'Press "Stay connected" to try again.',

    // deleting a space (SD-0). THE WORDS ARE THE FEATURE: every one of these
    // strings exists to stop somebody assuming a promise the system cannot
    // keep. "Delete" alone would be read as "delete for everyone", which no
    // local-first system can do and this one will not imply.
    'space.delete.btn': 'Delete from this device',
    'space.delete.title': 'Delete this space from this device?',
    'space.delete.what': 'Its messages and the media only this device holds will be removed from here.',
    'space.delete.others': 'Everyone else keeps their copy. Nobody is told.',
    'space.delete.back': 'A new invite can bring it back — the history you deleted will not come with it.',
    'space.delete.confirm': 'Delete here',
    'space.delete.working': 'Deleting…',
    'space.delete.failed': ({ why }) => `Not deleted: ${why}`,

    // conversation
    'conv.member': 'member',
    'conv.you': 'You',
    'conv.write': 'write into the space…',
    'conv.write_short': 'write…',
    'conv.send': 'send',
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

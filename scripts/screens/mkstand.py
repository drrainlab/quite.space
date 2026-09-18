"""Seed the screenshot stand: two nodes (8501 fatok / 8502 fbtok) already running from
~/.quiet-stand, one shared space, a short conversation with a reply, reactions,
two photos (one rides with the frame, one >8 MB that has to be fetched) and a file.
macOS only (uses sips for the JPEG preview). See README.md beside this file."""
import json,urllib.request,time,os,zlib,struct,math,uuid,sys,subprocess
A=('http://127.0.0.1:8501','fatok'); B=('http://127.0.0.1:8502','fbtok')
D=os.path.expanduser('~/.quiet-stand')
def call(n,path,method='GET',body=None):
    req=urllib.request.Request(n[0]+path,method=method,headers={'X-QP-Token':n[1],'Content-Type':'application/json'},data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(req) as r: return json.loads(r.read() or b'{}')
def png(w,h,fn,noise=False):
    rows=[]
    for y in range(h):
        row=bytearray([0])
        for x in range(w):
            if noise: row+=os.urandom(3)
            else:
                t=x/w; u=y/h
                row+=bytes([max(0,min(255,int(v))) for v in (40+120*u+60*math.sin(t*6),30+90*t,110+120*(1-u)*t+20)])
        rows.append(bytes(row))
    raw=b''.join(rows)
    ch=lambda t,d: struct.pack('>I',len(d))+t+d+struct.pack('>I',zlib.crc32(t+d)&0xffffffff)
    open(fn,'wb').write(b'\x89PNG\r\n\x1a\n'+ch(b'IHDR',struct.pack('>IIBBBBB',w,h,8,2,0,0,0))+ch(b'IDAT',zlib.compress(raw,0 if noise else 9))+ch(b'IEND',b''))
def block(n,tid,meta,file,preview=None):
    b='----qs'+uuid.uuid4().hex; parts=[]
    def part(name,ct,data,fn=None):
        h=f'--{b}\r\nContent-Disposition: form-data; name="{name}"'+(f'; filename="{fn}"' if fn else '')+f'\r\nContent-Type: {ct}\r\n\r\n'
        parts.append(h.encode()+data+b'\r\n')
    part('metadata','application/json',json.dumps(meta).encode())
    if preview: part('preview',meta['preview_mime'],open(preview,'rb').read(),'p')
    part('file',meta['media_type'],open(file,'rb').read(),'f')
    body=b''.join(parts)+f'--{b}--\r\n'.encode()
    req=urllib.request.Request(n[0]+f'/api/spaces/{tid}/blocks',method='POST',data=body,headers={'X-QP-Token':n[1],'Content-Type':'multipart/form-data; boundary='+b})
    with urllib.request.urlopen(req) as r: return json.loads(r.read())
def say(n,tid,text,reply=None):
    return call(n,f'/api/spaces/{tid}/messages','POST',{'text':text,**({'reply_to':reply} if reply else {})})['id']
def wait(n,tid,count,sec=60):
    for _ in range(sec):
        d=call(n,f'/api/spaces/{tid}/entries'); es=d if isinstance(d,list) else d.get('entries',[])
        if len(es)>=count: return es
        time.sleep(1)
    print('WARN only',len(es),'of',count); return es
tid=os.environ.get('TID')
if not tid:
    tid=call(A,'/api/spaces','POST',{'name':'pine listening room'}); tid=tid.get('id') or tid['space']['id']
    link=call(A,f'/api/spaces/{tid}/passes','POST',{'max_uses':2,'ttl_hours':24})['link']
    rid=call(B,'/api/join-requests','POST',{'pass':link})['request_id']
    for _ in range(120):
        if call(B,f'/api/join-requests/{rid}').get('status')=='ready': break
        time.sleep(1)
print('joined',tid)
png(320,200,D+'/small.png'); subprocess.run(['sips','-s','format','jpeg','-s','formatOptions','70','-Z','200',D+'/small.png','--out',D+'/thumb.jpg'],capture_output=True); png(1900,1900,D+'/huge.png',noise=True)
open(D+'/notes.bin','wb').write(os.urandom(12<<20))
if not os.environ.get('SKIP'):
    m1=say(B,tid,'Finally recorded the rain on the cabin roof.')
    wait(A,tid,1)
    call(A,f'/api/spaces/{tid}/reactions','POST',{'target':m1,'op':'set','reaction':{'kind':'semantic','key':'warmth'}})
    m2=say(A,tid,'This belongs on the next track.')
    say(A,tid,'And a second line right after it, so the rows group.')
    wait(B,tid,3)
    call(B,f'/api/spaces/{tid}/reactions','POST',{'target':m2,'op':'set','reaction':{'kind':'semantic','key':'spark'}})
else:
    m2=[e for e in wait(A,tid,3) if e.get('text','').startswith('This belongs')][0]['id']
block(B,tid,{'kind':'visual','size':os.path.getsize(D+'/small.png'),'media_type':'image/png','alt':'dusk over the ridge','caption':'quiet support','preview_mime':'image/jpeg','width':320,'height':200},D+'/small.png',D+'/thumb.jpg')
say(B,tid,'Длинное сообщение по-русски, чтобы пузырь перенёсся на две строки и было видно, как он держит текст и воздух вокруг него.',reply=m2)
block(B,tid,{'kind':'visual','size':os.path.getsize(D+'/huge.png'),'media_type':'image/png','alt':'the big one','caption':'a big one — tap it','preview_mime':'image/jpeg','width':1900,'height':1900},D+'/huge.png',D+'/thumb.jpg')
block(B,tid,{'kind':'file','size':12<<20,'media_type':'application/octet-stream','filename':'field-notes.bin'},D+'/notes.bin')
say(A,tid,'This is perfect. 💛')
say(B,tid,'https://quite.space/ru/relays/')
es=wait(A,tid,9,90); print(len(es),'entries on A'); open(D+'/tid.txt','w').write(tid)

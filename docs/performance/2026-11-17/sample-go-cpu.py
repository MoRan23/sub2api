# Read-only 20-second Linux/amd64 Go CPU sampling using perf_event_open.
# Usage: python3 sample-go-cpu.py PID
# Requires permission to profile PID. Does not change kernel settings or the target.
# Go 1.27 has a zeroed former textStart header; use ELF .text in that case.
import ctypes,os,struct,mmap,fcntl,time,collections,bisect,json
import sys
pid=int(sys.argv[1])
f=open('/proc/%d/exe'%pid,'rb'); binary=mmap.mmap(f.fileno(),0,access=mmap.ACCESS_READ)
shoff=struct.unpack_from('<Q',binary,40)[0]; entsize,nsec,stridx=struct.unpack_from('<HHH',binary,58)
sections=[struct.unpack_from('<IIQQQQIIQQ',binary,shoff+i*entsize) for i in range(nsec)]
st=sections[stridx]; names=binary[st[4]:st[4]+st[5]]
def cstr(buf,start):return buf[start:buf.find(b'\0',start)].decode('utf-8','replace')
pcsec=next(s for s in sections if cstr(names,s[0])=='.gopclntab')
textsec=next(s for s in sections if cstr(names,s[0])=='.text')
base=pcsec[4];magic=struct.unpack_from('<I',binary,base)[0]
nfunc,nfiles,oldtext,funcoff,cuoff,fileoff,pctoff,pclnoff=struct.unpack_from('<8Q',binary,base+8)
textstart=oldtext or textsec[3]
tbl=base+pclnoff;entries=[];fnames=[]
for i in range(nfunc):
 off,fo=struct.unpack_from('<II',binary,tbl+i*8);no=struct.unpack_from('<i',binary,tbl+fo+4)[0]
 entries.append(textstart+off);fnames.append(cstr(binary,base+funcoff+no))
entries.append(textstart+struct.unpack_from('<I',binary,tbl+nfunc*8)[0])
etype=struct.unpack_from('<H',binary,16)[0];bias=0
if etype==3:
 for l in open('/proc/%d/maps'%pid):
  parts=l.split()
  if len(parts)>5 and parts[5].endswith('/sub2api') and int(parts[2],16)==0:bias=int(parts[0].split('-')[0],16);break
def symbol(ip):
 ip-=bias;i=bisect.bisect_right(entries,ip)-1
 return fnames[i] if 0<=i<nfunc else '[outside Go text]'
libc=ctypes.CDLL(None,use_errno=True);rings=[];errors=[]
for tid in os.listdir('/proc/%d/task'%pid):
 a=ctypes.create_string_buffer(128)
 struct.pack_into('<IIQQQQQ',a,0,1,128,0,10000000,35,0,1|(1<<5)|(1<<6));struct.pack_into('<I',a,48,1)
 fd=libc.syscall(298,ctypes.byref(a),int(tid),-1,-1,0)
 if fd<0:errors.append(ctypes.get_errno());continue
 rings.append([fd,mmap.mmap(fd,4096+65536,mmap.MAP_SHARED,mmap.PROT_READ|mmap.PROT_WRITE),0])
leaf=collections.Counter();inclusive=collections.Counter();stacks=collections.Counter();lost=0;samples=0
def drain():
 global lost,samples
 for r in rings:
  fd,mm,tail=r;head=struct.unpack_from('<Q',mm,1024)[0];off,size=struct.unpack_from('<QQ',mm,1040)
  def read(at,n):
   k=at%size
   return mm[off+k:off+k+n] if k+n<=size else mm[off+k:off+size]+mm[off:off+n-(size-k)]
  while tail<head:
   typ,misc,n=struct.unpack('<IHH',read(tail,8));rec=read(tail,n);tail+=n
   if typ==9:
    ip=struct.unpack_from('<Q',rec,8)[0];nr=struct.unpack_from('<Q',rec,24)[0]
    ips=struct.unpack_from('<%dQ'%nr,rec,32) if nr else []
    call=[symbol(x) for x in ips if x<0xffff000000000000]
    leaf[symbol(ip)]+=1;samples+=1
    for name in set(call):inclusive[name]+=1
    stacks[tuple(call[:10])]+=1
   elif typ==2:lost+=struct.unpack_from('<Q',rec,16)[0]
  struct.pack_into('<Q',mm,1032,tail);r[2]=tail
for fd,mm,tail in rings:fcntl.ioctl(fd,0x2400,0)
start=time.monotonic()
while time.monotonic()-start<20:time.sleep(.1);drain()
for fd,mm,tail in rings:fcntl.ioctl(fd,0x2401,0)
drain()
print(json.dumps({'textstart':hex(textstart),'magic':hex(magic),'first_functions':fnames[:3],'sample_seconds':round(time.monotonic()-start,2),'threads':len(rings),'samples':samples,'lost':lost,'errors':errors,'leaf':leaf.most_common(30),'inclusive':inclusive.most_common(40),'application_hotspots':[(k,v) for k,v in inclusive.most_common() if '/internal/service.' in k or '/internal/securityaudit.' in k][:50],'stacks':stacks.most_common(15)},ensure_ascii=False))
for fd,mm,tail in rings:mm.close();os.close(fd)
binary.close();f.close()

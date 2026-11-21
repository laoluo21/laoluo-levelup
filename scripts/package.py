#!/usr/bin/env python3
"""Build only this plugin's code. Never package runtime state or workspace secrets."""
import hashlib,json,os,pathlib,re,subprocess,zipfile
root=pathlib.Path(__file__).resolve().parents[1]
source=(root/'internal/levelup/api.go').read_text()
version=re.search(r'const Version = "([^"]+)"',source)[1]
actions=re.findall(r'"([a-z_]+)"',re.search(r'var Actions = \[\]string\{(.*?)\}',source,re.S)[1])
manifest={'schema':3,'framework_api':'3.0.0','plugin_id':'laoluo-levelup','name':'laoluo-levelup','version':version,'min_framework':'3.0.7','author':'laoluo','notes':'【管理网页】默认仅监听服务器本机 http://127.0.0.1:18890/；外部访问请用HTTPS反向代理或SSH端口转发，不能在自己电脑直接用服务器的127.0.0.1。\n【首次密码】启动后在本插件运行日志查看一次性初始化码，打开网页填写初始化码，自行设置10–128位管理密码；没有默认密码，与Mengka和QQ密码无关。\n【修改密码】网页→访问管理→当前密码、新密码、确认新密码→更新管理密码；其他网页会话随即失效。\n【忘记密码】停止插件，备份本插件data目录，仅移走web-auth.json和web-first-access.txt，保留state.enc、state.key、config.json；重新启动后用新初始化码设置密码。\n完整映射及更新说明见包内《管理页面访问说明.md》和README.md。只用官方公开API；滑块到后台完成。','runtime':{'entry':'laoluo-levelup','transport':'native-ipc-v1','api_version':'1','admin':False,'actions':actions,'events':['friend_message']}}
(root/'mengka-plugin.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
(root/'dist').mkdir(exist_ok=True)
built=[]
for arch in ['amd64','arm64']:
 out=root/'.build'/('linux-'+arch);out.mkdir(parents=True,exist_ok=True)
 env=dict(os.environ,GOOS='linux',GOARCH=arch,CGO_ENABLED='0')
 subprocess.run(['go','build','-trimpath','-ldflags=-s -w','-o',str(out/'laoluo-levelup'),'./cmd/laoluo-levelup'],cwd=root,env=env,check=True)
 archive=root/'dist'/f'laoluo-levelup-v{version}-linux-{arch}.zip'
 contents={'mengka-plugin.json':(root/'mengka-plugin.json').read_bytes(),'config.json':(root/'config.json').read_bytes(),'README.md':(root/'README.md').read_bytes(),'管理页面访问说明.md':(root/'管理页面访问说明.md').read_bytes(),'LICENSE':(root/'LICENSE').read_bytes(),'laoluo-levelup':(out/'laoluo-levelup').read_bytes()}
 for p in (root/'LICENSES').glob('*'):contents['LICENSES/'+p.name]=p.read_bytes()
 with zipfile.ZipFile(archive,'w',compression=zipfile.ZIP_DEFLATED,compresslevel=9) as z:
  for name,raw in contents.items():
   info=zipfile.ZipInfo(name,date_time=(2026,10,27,0,0,0));info.create_system=3;info.external_attr=((0o100755 if name=='laoluo-levelup' else 0o100644)<<16);info.compress_type=zipfile.ZIP_DEFLATED;z.writestr(info,raw)
 built.append(archive)
sourcezip=root/'dist'/f'laoluo-levelup-v{version}-source.zip'
with zipfile.ZipFile(sourcezip,'w',compression=zipfile.ZIP_DEFLATED) as z:
 for p in sorted(root.rglob('*')):
  rel=p.relative_to(root)
  if not p.is_file() or any(part in ['.build','dist','data','.git','previews'] for part in rel.parts) or p.suffix in ['.enc','.key'] or (p.suffix == '.html' and rel != pathlib.Path('internal/levelup/web.html')):continue
  z.write(p,'laoluo-levelup/'+str(rel))
built.append(sourcezip)
lines=[hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name for p in built]
(root/'dist/SHA256SUMS').write_text('\n'.join(lines)+'\n')
print('\n'.join(str(p) for p in built))

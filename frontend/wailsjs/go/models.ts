export namespace host {
	
	export class Host {
	    id: string;
	    name: string;
	    addr: string;
	    port: number;
	    user: string;
	    password: string;
	    keyPath: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	    clusterId?: string;
	    clusterName?: string;
	    jumpHostIds?: string[];
	    jumpHostId?: string;
	    lizExpDays?: number;
	    rootExpDays?: number;
	    // Go type: time
	    pwCheckedAt?: any;
	
	    static createFrom(source: any = {}) {
	        return new Host(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.addr = source["addr"];
	        this.port = source["port"];
	        this.user = source["user"];
	        this.password = source["password"];
	        this.keyPath = source["keyPath"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.clusterId = source["clusterId"];
	        this.clusterName = source["clusterName"];
	        this.jumpHostIds = source["jumpHostIds"];
	        this.jumpHostId = source["jumpHostId"];
	        this.lizExpDays = source["lizExpDays"];
	        this.rootExpDays = source["rootExpDays"];
	        this.pwCheckedAt = this.convertValues(source["pwCheckedAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class ClusterConnectResult {
	    hostId: string;
	    caps: monitor.Capabilities;
	    err: string;
	
	    static createFrom(source: any = {}) {
	        return new ClusterConnectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.caps = this.convertValues(source["caps"], monitor.Capabilities);
	        this.err = source["err"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogCollectHostResult {
	    hostId: string;
	    name: string;
	    serverDir: string;
	    path: string;
	    bytes: number;
	    files: number;
	    cleaned: boolean;
	    err?: string;
	    leftOnHost?: string;
	    collectId?: string;
	    target?: string;
	
	    static createFrom(source: any = {}) {
	        return new LogCollectHostResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.name = source["name"];
	        this.serverDir = source["serverDir"];
	        this.path = source["path"];
	        this.bytes = source["bytes"];
	        this.files = source["files"];
	        this.cleaned = source["cleaned"];
	        this.err = source["err"];
	        this.leftOnHost = source["leftOnHost"];
	        this.collectId = source["collectId"];
	        this.target = source["target"];
	    }
	}
	export class LogCollectResult {
	    dir: string;
	    hosts: LogCollectHostResult[];
	    ok: number;
	    failed: number;
	
	    static createFrom(source: any = {}) {
	        return new LogCollectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dir = source["dir"];
	        this.hosts = this.convertValues(source["hosts"], LogCollectHostResult);
	        this.ok = source["ok"];
	        this.failed = source["failed"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogCollectTask {
	    hostId: string;
	    req: monitor.LogRequest;
	
	    static createFrom(source: any = {}) {
	        return new LogCollectTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.req = this.convertValues(source["req"], monitor.LogRequest);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogHostInfo {
	    id: string;
	    name: string;
	    frames: number;
	    startT: number;
	    endT: number;
	
	    static createFrom(source: any = {}) {
	        return new LogHostInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.frames = source["frames"];
	        this.startT = source["startT"];
	        this.endT = source["endT"];
	    }
	}
	export class LogMeta {
	    path: string;
	    hosts: LogHostInfo[];
	    stride: number;
	
	    static createFrom(source: any = {}) {
	        return new LogMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.hosts = this.convertValues(source["hosts"], LogHostInfo);
	        this.stride = source["stride"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace monitor {
	
	export class RecTarget {
	    path: string;
	    mount: string;
	    totalBytes: number;
	    freeBytes: number;
	    writable: boolean;
	    needsSudo: boolean;
	    fsType: string;
	
	    static createFrom(source: any = {}) {
	        return new RecTarget(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.mount = source["mount"];
	        this.totalBytes = source["totalBytes"];
	        this.freeBytes = source["freeBytes"];
	        this.writable = source["writable"];
	        this.needsSudo = source["needsSudo"];
	        this.fsType = source["fsType"];
	    }
	}
	export class NIC {
	    name: string;
	    state: string;
	    mac: string;
	    ipv4: string;
	    speedMb: number;
	    mtu: number;
	    up: boolean;
	    virtual: boolean;
	    loopback: boolean;
	    master: string;
	    isMaster: boolean;
	
	    static createFrom(source: any = {}) {
	        return new NIC(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.state = source["state"];
	        this.mac = source["mac"];
	        this.ipv4 = source["ipv4"];
	        this.speedMb = source["speedMb"];
	        this.mtu = source["mtu"];
	        this.up = source["up"];
	        this.virtual = source["virtual"];
	        this.loopback = source["loopback"];
	        this.master = source["master"];
	        this.isMaster = source["isMaster"];
	    }
	}
	export class CapEnv {
	    elevated: boolean;
	    tcpdump: boolean;
	    tcpdumpPath: string;
	    version: string;
	    nics: NIC[];
	    targets: RecTarget[];
	    maxSec: number;
	    maxMB: number;
	    maxPackets: number;
	    existingCount: number;
	    existingBytes: number;
	    running: number;
	    owner: string;
	    ownerUid: number;
	
	    static createFrom(source: any = {}) {
	        return new CapEnv(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.elevated = source["elevated"];
	        this.tcpdump = source["tcpdump"];
	        this.tcpdumpPath = source["tcpdumpPath"];
	        this.version = source["version"];
	        this.nics = this.convertValues(source["nics"], NIC);
	        this.targets = this.convertValues(source["targets"], RecTarget);
	        this.maxSec = source["maxSec"];
	        this.maxMB = source["maxMB"];
	        this.maxPackets = source["maxPackets"];
	        this.existingCount = source["existingCount"];
	        this.existingBytes = source["existingBytes"];
	        this.running = source["running"];
	        this.owner = source["owner"];
	        this.ownerUid = source["ownerUid"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CapMeta {
	    id: string;
	    hostId: string;
	    hostName: string;
	    file: string;
	    iface: string;
	    filter: string;
	    portSpec: string;
	    portsRaw: string;
	    hostSpec: string;
	    hostsRaw: string;
	    snapLen: number;
	    maxSec: number;
	    minFreeMB: number;
	    maxPackets: number;
	    maxBytes: number;
	    promisc: boolean;
	    startT: number;
	    plannedEndT: number;
	    owner: string;
	    ownerUid: number;
	    startedBy: string;
	    status: string;
	    doneReason: string;
	    err: string;
	    linkType: string;
	    sizeBytes: number;
	    lastT: number;
	    uncertain: boolean;
	    captured: number;
	    received: number;
	    droppedKern: number;
	    droppedIf: number;
	    localPath: string;
	
	    static createFrom(source: any = {}) {
	        return new CapMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.file = source["file"];
	        this.iface = source["iface"];
	        this.filter = source["filter"];
	        this.portSpec = source["portSpec"];
	        this.portsRaw = source["portsRaw"];
	        this.hostSpec = source["hostSpec"];
	        this.hostsRaw = source["hostsRaw"];
	        this.snapLen = source["snapLen"];
	        this.maxSec = source["maxSec"];
	        this.minFreeMB = source["minFreeMB"];
	        this.maxPackets = source["maxPackets"];
	        this.maxBytes = source["maxBytes"];
	        this.promisc = source["promisc"];
	        this.startT = source["startT"];
	        this.plannedEndT = source["plannedEndT"];
	        this.owner = source["owner"];
	        this.ownerUid = source["ownerUid"];
	        this.startedBy = source["startedBy"];
	        this.status = source["status"];
	        this.doneReason = source["doneReason"];
	        this.err = source["err"];
	        this.linkType = source["linkType"];
	        this.sizeBytes = source["sizeBytes"];
	        this.lastT = source["lastT"];
	        this.uncertain = source["uncertain"];
	        this.captured = source["captured"];
	        this.received = source["received"];
	        this.droppedKern = source["droppedKern"];
	        this.droppedIf = source["droppedIf"];
	        this.localPath = source["localPath"];
	    }
	}
	export class CapRequest {
	    iface: string;
	    portSpec: string;
	    hostSpec: string;
	    targetDir: string;
	    maxSec: number;
	    maxMB: number;
	    minFreeMB: number;
	    maxPackets: number;
	    snapLen: number;
	    promisc: boolean;
	    noFilter: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CapRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.iface = source["iface"];
	        this.portSpec = source["portSpec"];
	        this.hostSpec = source["hostSpec"];
	        this.targetDir = source["targetDir"];
	        this.maxSec = source["maxSec"];
	        this.maxMB = source["maxMB"];
	        this.minFreeMB = source["minFreeMB"];
	        this.maxPackets = source["maxPackets"];
	        this.snapLen = source["snapLen"];
	        this.promisc = source["promisc"];
	        this.noFilter = source["noFilter"];
	    }
	}
	export class Capabilities {
	    uid: number;
	    os: string;
	    rhel: string;
	    nethogs: boolean;
	    pidstat: boolean;
	    sudo: boolean;
	    stageDir: string;
	
	    static createFrom(source: any = {}) {
	        return new Capabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.uid = source["uid"];
	        this.os = source["os"];
	        this.rhel = source["rhel"];
	        this.nethogs = source["nethogs"];
	        this.pidstat = source["pidstat"];
	        this.sudo = source["sudo"];
	        this.stageDir = source["stageDir"];
	    }
	}
	export class DiskStat {
	    mount: string;
	    dev: string;
	    fsType: string;
	    total: number;
	    free: number;
	    used: number;
	    rBps: number;
	    wBps: number;
	    busy: number;
	    kind: string;
	
	    static createFrom(source: any = {}) {
	        return new DiskStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mount = source["mount"];
	        this.dev = source["dev"];
	        this.fsType = source["fsType"];
	        this.total = source["total"];
	        this.free = source["free"];
	        this.used = source["used"];
	        this.rBps = source["rBps"];
	        this.wBps = source["wBps"];
	        this.busy = source["busy"];
	        this.kind = source["kind"];
	    }
	}
	export class Proc {
	    pid: number;
	    ppid: number;
	    name: string;
	    user: string;
	    service: string;
	    state: string;
	    cpu: number;
	    memPct: number;
	    rssKiB: number;
	    diskR: number;
	    diskW: number;
	    net: number;
	    threads: number;
	    start: number;
	
	    static createFrom(source: any = {}) {
	        return new Proc(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pid = source["pid"];
	        this.ppid = source["ppid"];
	        this.name = source["name"];
	        this.user = source["user"];
	        this.service = source["service"];
	        this.state = source["state"];
	        this.cpu = source["cpu"];
	        this.memPct = source["memPct"];
	        this.rssKiB = source["rssKiB"];
	        this.diskR = source["diskR"];
	        this.diskW = source["diskW"];
	        this.net = source["net"];
	        this.threads = source["threads"];
	        this.start = source["start"];
	    }
	}
	export class NetStat {
	    name: string;
	    rxBps: number;
	    txBps: number;
	    speed: number;
	
	    static createFrom(source: any = {}) {
	        return new NetStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.rxBps = source["rxBps"];
	        this.txBps = source["txBps"];
	        this.speed = source["speed"];
	    }
	}
	export class Frame {
	    hostId: string;
	    t: number;
	    ncpu: number;
	    memTotal: number;
	    memUsed: number;
	    cpu: number;
	    mem: number;
	    swapTotal: number;
	    swapUsed: number;
	    netRx: number;
	    netTx: number;
	    netSpeed: number;
	    nets: NetStat[];
	    disks: DiskStat[];
	    procs: Proc[];
	
	    static createFrom(source: any = {}) {
	        return new Frame(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.t = source["t"];
	        this.ncpu = source["ncpu"];
	        this.memTotal = source["memTotal"];
	        this.memUsed = source["memUsed"];
	        this.cpu = source["cpu"];
	        this.mem = source["mem"];
	        this.swapTotal = source["swapTotal"];
	        this.swapUsed = source["swapUsed"];
	        this.netRx = source["netRx"];
	        this.netTx = source["netTx"];
	        this.netSpeed = source["netSpeed"];
	        this.nets = this.convertValues(source["nets"], NetStat);
	        this.disks = this.convertValues(source["disks"], DiskStat);
	        this.procs = this.convertValues(source["procs"], Proc);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogModuleDef {
	    name: string;
	    paths?: string[];
	    match?: string;
	    recursive?: boolean;
	    exclude?: string[];
	    needsRoot?: boolean;
	    cmd?: string;
	    outFile?: string;
	
	    static createFrom(source: any = {}) {
	        return new LogModuleDef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.paths = source["paths"];
	        this.match = source["match"];
	        this.recursive = source["recursive"];
	        this.exclude = source["exclude"];
	        this.needsRoot = source["needsRoot"];
	        this.cmd = source["cmd"];
	        this.outFile = source["outFile"];
	    }
	}
	export class LogCategoryDef {
	    key: string;
	    label: string;
	    discoverGlobs?: string[];
	    modules?: LogModuleDef[];
	
	    static createFrom(source: any = {}) {
	        return new LogCategoryDef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.discoverGlobs = source["discoverGlobs"];
	        this.modules = this.convertValues(source["modules"], LogModuleDef);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogCatalog {
	    version: number;
	    categories: LogCategoryDef[];
	
	    static createFrom(source: any = {}) {
	        return new LogCatalog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.categories = this.convertValues(source["categories"], LogCategoryDef);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class LogEntry {
	    hostId: string;
	    category: string;
	    module: string;
	    source: string;
	    rel: string;
	    sizeBytes: number;
	    mtimeMs: number;
	    cut: boolean;
	    cutFrom?: string;
	    cutTo?: string;
	    firstMs?: number;
	    lastMs?: number;
	    skipped?: string;
	    isCmd?: boolean;
	    dupOf?: string;
	    action?: string;
	    note?: string;
	    arch?: string;
	    archWhole?: string;
	
	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.category = source["category"];
	        this.module = source["module"];
	        this.source = source["source"];
	        this.rel = source["rel"];
	        this.sizeBytes = source["sizeBytes"];
	        this.mtimeMs = source["mtimeMs"];
	        this.cut = source["cut"];
	        this.cutFrom = source["cutFrom"];
	        this.cutTo = source["cutTo"];
	        this.firstMs = source["firstMs"];
	        this.lastMs = source["lastMs"];
	        this.skipped = source["skipped"];
	        this.isCmd = source["isCmd"];
	        this.dupOf = source["dupOf"];
	        this.action = source["action"];
	        this.note = source["note"];
	        this.arch = source["arch"];
	        this.archWhole = source["archWhole"];
	    }
	}
	export class LogLeftover {
	    hostId: string;
	    id: string;
	    path: string;
	    kind: string;
	    bytes: number;
	    mtimeMs: number;
	
	    static createFrom(source: any = {}) {
	        return new LogLeftover(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.id = source["id"];
	        this.path = source["path"];
	        this.kind = source["kind"];
	        this.bytes = source["bytes"];
	        this.mtimeMs = source["mtimeMs"];
	    }
	}
	
	export class LogModuleStat {
	    category: string;
	    module: string;
	    dir: string;
	    realDir: string;
	    status: string;
	    files: number;
	    bytes: number;
	    oldestMs: number;
	    newestMs: number;
	    compressedBytes: number;
	    isFile: boolean;
	    needsRoot: boolean;
	    isCmd: boolean;
	    recursive: boolean;
	    match?: string;
	    dupOfDir?: string;
	
	    static createFrom(source: any = {}) {
	        return new LogModuleStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.category = source["category"];
	        this.module = source["module"];
	        this.dir = source["dir"];
	        this.realDir = source["realDir"];
	        this.status = source["status"];
	        this.files = source["files"];
	        this.bytes = source["bytes"];
	        this.oldestMs = source["oldestMs"];
	        this.newestMs = source["newestMs"];
	        this.compressedBytes = source["compressedBytes"];
	        this.isFile = source["isFile"];
	        this.needsRoot = source["needsRoot"];
	        this.isCmd = source["isCmd"];
	        this.recursive = source["recursive"];
	        this.match = source["match"];
	        this.dupOfDir = source["dupOfDir"];
	    }
	}
	export class LogPick {
	    category: string;
	    module: string;
	    dir: string;
	
	    static createFrom(source: any = {}) {
	        return new LogPick(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.category = source["category"];
	        this.module = source["module"];
	        this.dir = source["dir"];
	    }
	}
	export class LogPlan {
	    hostId: string;
	    hostname: string;
	    serverDir: string;
	    entries: LogEntry[];
	    files: number;
	    totalBytes: number;
	    estArchive: number;
	    needBytes: number;
	    freeBytes: number;
	    target: string;
	    targets: RecTarget[];
	    ok: boolean;
	    reason: string;
	    elevated: boolean;
	    fromMs: number;
	    toMs: number;
	
	    static createFrom(source: any = {}) {
	        return new LogPlan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.hostname = source["hostname"];
	        this.serverDir = source["serverDir"];
	        this.entries = this.convertValues(source["entries"], LogEntry);
	        this.files = source["files"];
	        this.totalBytes = source["totalBytes"];
	        this.estArchive = source["estArchive"];
	        this.needBytes = source["needBytes"];
	        this.freeBytes = source["freeBytes"];
	        this.target = source["target"];
	        this.targets = this.convertValues(source["targets"], RecTarget);
	        this.ok = source["ok"];
	        this.reason = source["reason"];
	        this.elevated = source["elevated"];
	        this.fromMs = source["fromMs"];
	        this.toMs = source["toMs"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogRequest {
	    picks: LogPick[];
	    fromMs: number;
	    toMs: number;
	    recentDays: number;
	    target: string;
	    serverDir: string;
	    hostName: string;
	    addr: string;
	    tz: string;
	    journalUnits: string[];
	    journalMaxBytes: number;
	
	    static createFrom(source: any = {}) {
	        return new LogRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.picks = this.convertValues(source["picks"], LogPick);
	        this.fromMs = source["fromMs"];
	        this.toMs = source["toMs"];
	        this.recentDays = source["recentDays"];
	        this.target = source["target"];
	        this.serverDir = source["serverDir"];
	        this.hostName = source["hostName"];
	        this.addr = source["addr"];
	        this.tz = source["tz"];
	        this.journalUnits = source["journalUnits"];
	        this.journalMaxBytes = source["journalMaxBytes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogSurvey {
	    hostId: string;
	    hostName: string;
	    hostname: string;
	    dirName: string;
	    elevated: boolean;
	    modules: LogModuleStat[];
	    targets: RecTarget[];
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new LogSurvey(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.hostname = source["hostname"];
	        this.dirName = source["dirName"];
	        this.elevated = source["elevated"];
	        this.modules = this.convertValues(source["modules"], LogModuleStat);
	        this.targets = this.convertValues(source["targets"], RecTarget);
	        this.err = source["err"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class PwStatus {
	    hasLiz: boolean;
	    hasRoot: boolean;
	    lizExpDays: number;
	    rootExpDays: number;
	    todayDays: number;
	
	    static createFrom(source: any = {}) {
	        return new PwStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hasLiz = source["hasLiz"];
	        this.hasRoot = source["hasRoot"];
	        this.lizExpDays = source["lizExpDays"];
	        this.rootExpDays = source["rootExpDays"];
	        this.todayDays = source["todayDays"];
	    }
	}
	export class RecEstimate {
	    targets: RecTarget[];
	    probeSec: number;
	    probeBytes: number;
	    frames: number;
	    bytesPerHour: number;
	    bytesPerDay: number;
	
	    static createFrom(source: any = {}) {
	        return new RecEstimate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.targets = this.convertValues(source["targets"], RecTarget);
	        this.probeSec = source["probeSec"];
	        this.probeBytes = source["probeBytes"];
	        this.frames = source["frames"];
	        this.bytesPerHour = source["bytesPerHour"];
	        this.bytesPerDay = source["bytesPerDay"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RecMeta {
	    id: string;
	    hostId: string;
	    hostName: string;
	    file: string;
	    startT: number;
	    plannedEndT: number;
	    durationSec: number;
	    intervalSec: number;
	    status: string;
	    sizeBytes: number;
	    lastT: number;
	    doneReason: string;
	
	    static createFrom(source: any = {}) {
	        return new RecMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.file = source["file"];
	        this.startT = source["startT"];
	        this.plannedEndT = source["plannedEndT"];
	        this.durationSec = source["durationSec"];
	        this.intervalSec = source["intervalSec"];
	        this.status = source["status"];
	        this.sizeBytes = source["sizeBytes"];
	        this.lastT = source["lastT"];
	        this.doneReason = source["doneReason"];
	    }
	}

}

export namespace pwledger {
	
	export class Config {
	    tempPassword: string;
	    expiryWarnDays: number;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tempPassword = source["tempPassword"];
	        this.expiryWarnDays = source["expiryWarnDays"];
	    }
	}
	export class Entry {
	    id: string;
	    hostId: string;
	    hostName: string;
	    addr: string;
	    account: string;
	    op: string;
	    step: string;
	    password: string;
	    status: string;
	    err?: string;
	    // Go type: time
	    at: any;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.addr = source["addr"];
	        this.account = source["account"];
	        this.op = source["op"];
	        this.step = source["step"];
	        this.password = source["password"];
	        this.status = source["status"];
	        this.err = source["err"];
	        this.at = this.convertValues(source["at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace record {
	
	export class Point {
	    t: number;
	    cpu: number;
	    memPct: number;
	    rssKiB: number;
	    diskR: number;
	    diskW: number;
	
	    static createFrom(source: any = {}) {
	        return new Point(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.t = source["t"];
	        this.cpu = source["cpu"];
	        this.memPct = source["memPct"];
	        this.rssKiB = source["rssKiB"];
	        this.diskR = source["diskR"];
	        this.diskW = source["diskW"];
	    }
	}

}

export namespace updater {
	
	export class UpdateInfo {
	    available: boolean;
	    currentVersion: string;
	    latestVersion: string;
	    releaseNotes: string;
	    downloadUrl: string;
	    publishedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.currentVersion = source["currentVersion"];
	        this.latestVersion = source["latestVersion"];
	        this.releaseNotes = source["releaseNotes"];
	        this.downloadUrl = source["downloadUrl"];
	        this.publishedAt = source["publishedAt"];
	    }
	}
	export class AutoUpdateResult {
	    applying: boolean;
	    blocked: boolean;
	    info: UpdateInfo;
	
	    static createFrom(source: any = {}) {
	        return new AutoUpdateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.applying = source["applying"];
	        this.blocked = source["blocked"];
	        this.info = this.convertValues(source["info"], UpdateInfo);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PendingNotes {
	    version: string;
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingNotes(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.notes = source["notes"];
	    }
	}

}


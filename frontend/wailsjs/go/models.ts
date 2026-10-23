export namespace gclog {
	
	export class Analysis {
	    verdict: string;
	    headline: string;
	    method: string;
	    slopeMBh: number;
	    riseMB: number;
	    floorFromMB: number;
	    floorToMB: number;
	    rho: number;
	    fromMs: number;
	    toMs: number;
	    warmupMin: number;
	    spanH: number;
	    points: number;
	    reclaims: number;
	    fullGCs: number;
	    exhausted: number;
	    maxPauseMs: number;
	    gcTimePct: number;
	    capMB: number;
	    maxHeapMB: number;
	    hoursToMax: number;
	    notes: string[];
	
	    static createFrom(source: any = {}) {
	        return new Analysis(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.verdict = source["verdict"];
	        this.headline = source["headline"];
	        this.method = source["method"];
	        this.slopeMBh = source["slopeMBh"];
	        this.riseMB = source["riseMB"];
	        this.floorFromMB = source["floorFromMB"];
	        this.floorToMB = source["floorToMB"];
	        this.rho = source["rho"];
	        this.fromMs = source["fromMs"];
	        this.toMs = source["toMs"];
	        this.warmupMin = source["warmupMin"];
	        this.spanH = source["spanH"];
	        this.points = source["points"];
	        this.reclaims = source["reclaims"];
	        this.fullGCs = source["fullGCs"];
	        this.exhausted = source["exhausted"];
	        this.maxPauseMs = source["maxPauseMs"];
	        this.gcTimePct = source["gcTimePct"];
	        this.capMB = source["capMB"];
	        this.maxHeapMB = source["maxHeapMB"];
	        this.hoursToMax = source["hoursToMax"];
	        this.notes = source["notes"];
	    }
	}
	export class Point {
	    t: number;
	    mb: number;
	
	    static createFrom(source: any = {}) {
	        return new Point(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.t = source["t"];
	        this.mb = source["mb"];
	    }
	}
	export class Run {
	    startMs: number;
	    fromMs: number;
	    toMs: number;
	    events: number;
	
	    static createFrom(source: any = {}) {
	        return new Run(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.startMs = source["startMs"];
	        this.fromMs = source["fromMs"];
	        this.toMs = source["toMs"];
	        this.events = source["events"];
	    }
	}
	export class Chart {
	    run: Run;
	    after: Point[];
	    cap: Point[];
	    floor: Point[];
	    reclaims: Point[];
	    fitFrom: Point;
	    fitTo: Point;
	    warmupToMs: number;
	    maxHeapMB: number;
	
	    static createFrom(source: any = {}) {
	        return new Chart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.run = this.convertValues(source["run"], Run);
	        this.after = this.convertValues(source["after"], Point);
	        this.cap = this.convertValues(source["cap"], Point);
	        this.floor = this.convertValues(source["floor"], Point);
	        this.reclaims = this.convertValues(source["reclaims"], Point);
	        this.fitFrom = this.convertValues(source["fitFrom"], Point);
	        this.fitTo = this.convertValues(source["fitTo"], Point);
	        this.warmupToMs = source["warmupToMs"];
	        this.maxHeapMB = source["maxHeapMB"];
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
	
	export class CapacityHostReport {
	    hostId: string;
	    hostName: string;
	    err: string;
	    result: monitor.CapacityResult;
	
	    static createFrom(source: any = {}) {
	        return new CapacityHostReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.err = source["err"];
	        this.result = this.convertValues(source["result"], monitor.CapacityResult);
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
	export class GcChartResult {
	    chart: gclog.Chart;
	    analysis: gclog.Analysis;
	
	    static createFrom(source: any = {}) {
	        return new GcChartResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chart = this.convertValues(source["chart"], gclog.Chart);
	        this.analysis = this.convertValues(source["analysis"], gclog.Analysis);
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
	export class ProcTrend {
	    samples: number;
	    spanH: number;
	    rssFromMB: number;
	    threadFrom: number;
	    fdFrom: number;
	
	    static createFrom(source: any = {}) {
	        return new ProcTrend(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.samples = source["samples"];
	        this.spanH = source["spanH"];
	        this.rssFromMB = source["rssFromMB"];
	        this.threadFrom = source["threadFrom"];
	        this.fdFrom = source["fdFrom"];
	    }
	}
	export class GcModuleReport {
	    key: string;
	    name: string;
	    user: string;
	    pid: number;
	    running: boolean;
	    gcLog: string;
	    err: string;
	    upSec: number;
	    rssMB: number;
	    threads: number;
	    fds: number;
	    proc: ProcTrend;
	    added: number;
	    archives: number;
	    archivesRead: number;
	    archived: number;
	    runs: gclog.Run[];
	    analysis: gclog.Analysis;
	
	    static createFrom(source: any = {}) {
	        return new GcModuleReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.name = source["name"];
	        this.user = source["user"];
	        this.pid = source["pid"];
	        this.running = source["running"];
	        this.gcLog = source["gcLog"];
	        this.err = source["err"];
	        this.upSec = source["upSec"];
	        this.rssMB = source["rssMB"];
	        this.threads = source["threads"];
	        this.fds = source["fds"];
	        this.proc = this.convertValues(source["proc"], ProcTrend);
	        this.added = source["added"];
	        this.archives = source["archives"];
	        this.archivesRead = source["archivesRead"];
	        this.archived = source["archived"];
	        this.runs = this.convertValues(source["runs"], gclog.Run);
	        this.analysis = this.convertValues(source["analysis"], gclog.Analysis);
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
	export class GcHostReport {
	    hostId: string;
	    hostName: string;
	    err: string;
	    fetchedAt: number;
	    modules: GcModuleReport[];
	
	    static createFrom(source: any = {}) {
	        return new GcHostReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.hostName = source["hostName"];
	        this.err = source["err"];
	        this.fetchedAt = source["fetchedAt"];
	        this.modules = this.convertValues(source["modules"], GcModuleReport);
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
	    canceled?: boolean;
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
	        this.canceled = source["canceled"];
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
	    canceled: number;
	
	    static createFrom(source: any = {}) {
	        return new LogCollectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dir = source["dir"];
	        this.hosts = this.convertValues(source["hosts"], LogCollectHostResult);
	        this.ok = source["ok"];
	        this.failed = source["failed"];
	        this.canceled = source["canceled"];
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
	export class CapacityKafkaPart {
	    part: string;
	    rate: number;
	    pct: number;
	    leader: string;
	    isr: string;
	    isrOk: boolean;
	    isrKnown: boolean;
	    verdict: string;
	
	    static createFrom(source: any = {}) {
	        return new CapacityKafkaPart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.part = source["part"];
	        this.rate = source["rate"];
	        this.pct = source["pct"];
	        this.leader = source["leader"];
	        this.isr = source["isr"];
	        this.isrOk = source["isrOk"];
	        this.isrKnown = source["isrKnown"];
	        this.verdict = source["verdict"];
	    }
	}
	export class CapacityKafkaThread {
	    label: string;
	    busy: number;
	    total: number;
	    max: number;
	    avg: number;
	    sum: number;
	    verdict: string;
	
	    static createFrom(source: any = {}) {
	        return new CapacityKafkaThread(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.busy = source["busy"];
	        this.total = source["total"];
	        this.max = source["max"];
	        this.avg = source["avg"];
	        this.sum = source["sum"];
	        this.verdict = source["verdict"];
	    }
	}
	export class CapacityKafka {
	    at: string;
	    pid: number;
	    netThreads: string;
	    ioThreads: string;
	    heap: string;
	    cores: number;
	    mode: string;
	    jstackMs: number;
	    why: string;
	    threads: CapacityKafkaThread[];
	    brokerCpu: number;
	    gcPct: number;
	    hasGc: boolean;
	    parts: CapacityKafkaPart[];
	    topicTotal: number;
	    partLimit: number;
	    topic: string;
	    warnings: string[];
	    err: string;
	
	    static createFrom(source: any = {}) {
	        return new CapacityKafka(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = source["at"];
	        this.pid = source["pid"];
	        this.netThreads = source["netThreads"];
	        this.ioThreads = source["ioThreads"];
	        this.heap = source["heap"];
	        this.cores = source["cores"];
	        this.mode = source["mode"];
	        this.jstackMs = source["jstackMs"];
	        this.why = source["why"];
	        this.threads = this.convertValues(source["threads"], CapacityKafkaThread);
	        this.brokerCpu = source["brokerCpu"];
	        this.gcPct = source["gcPct"];
	        this.hasGc = source["hasGc"];
	        this.parts = this.convertValues(source["parts"], CapacityKafkaPart);
	        this.topicTotal = source["topicTotal"];
	        this.partLimit = source["partLimit"];
	        this.topic = source["topic"];
	        this.warnings = source["warnings"];
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
	
	
	export class CapacityOptions {
	    seconds: number;
	    topic: string;
	    jstack: boolean;
	    kafka: boolean;
	    redis: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CapacityOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seconds = source["seconds"];
	        this.topic = source["topic"];
	        this.jstack = source["jstack"];
	        this.kafka = source["kafka"];
	        this.redis = source["redis"];
	    }
	}
	export class CapacityRedisSample {
	    at: string;
	    cmd: number;
	    set: number;
	    mainPct: number;
	    us: number;
	    cap: number;
	    freePct: number;
	    verdict: string;
	
	    static createFrom(source: any = {}) {
	        return new CapacityRedisSample(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = source["at"];
	        this.cmd = source["cmd"];
	        this.set = source["set"];
	        this.mainPct = source["mainPct"];
	        this.us = source["us"];
	        this.cap = source["cap"];
	        this.freePct = source["freePct"];
	        this.verdict = source["verdict"];
	    }
	}
	export class CapacityRedis {
	    port: number;
	    version: string;
	    role: string;
	    replicas: number;
	    backlog: string;
	    ioThreads: string;
	    samples: CapacityRedisSample[];
	    warnings: string[];
	    err: string;
	
	    static createFrom(source: any = {}) {
	        return new CapacityRedis(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.port = source["port"];
	        this.version = source["version"];
	        this.role = source["role"];
	        this.replicas = source["replicas"];
	        this.backlog = source["backlog"];
	        this.ioThreads = source["ioThreads"];
	        this.samples = this.convertValues(source["samples"], CapacityRedisSample);
	        this.warnings = source["warnings"];
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
	
	export class CapacityResult {
	    nowMs: number;
	    kafka?: CapacityKafka;
	    redis: CapacityRedis[];
	
	    static createFrom(source: any = {}) {
	        return new CapacityResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nowMs = source["nowMs"];
	        this.kafka = this.convertValues(source["kafka"], CapacityKafka);
	        this.redis = this.convertValues(source["redis"], CapacityRedis);
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

export namespace statsreg {
	
	export class Config {
	    dbHost: string;
	    dbPort: number;
	    dbUser: string;
	    dbPassword: string;
	    redisHost: string;
	    redisPort: number;
	    redisUser: string;
	    redisPassword: string;
	    redisDB: number;
	    brokers: string;
	    kafkaSecurity: string;
	    kafkaUser: string;
	    kafkaPassword: string;
	    kafkaHosts: string;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dbHost = source["dbHost"];
	        this.dbPort = source["dbPort"];
	        this.dbUser = source["dbUser"];
	        this.dbPassword = source["dbPassword"];
	        this.redisHost = source["redisHost"];
	        this.redisPort = source["redisPort"];
	        this.redisUser = source["redisUser"];
	        this.redisPassword = source["redisPassword"];
	        this.redisDB = source["redisDB"];
	        this.brokers = source["brokers"];
	        this.kafkaSecurity = source["kafkaSecurity"];
	        this.kafkaUser = source["kafkaUser"];
	        this.kafkaPassword = source["kafkaPassword"];
	        this.kafkaHosts = source["kafkaHosts"];
	    }
	}
	export class DeletePoint {
	    deviceId: number;
	    checkpointId: number;
	    name: string;
	    key: string;
	
	    static createFrom(source: any = {}) {
	        return new DeletePoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.checkpointId = source["checkpointId"];
	        this.name = source["name"];
	        this.key = source["key"];
	    }
	}
	export class ManagedDevice {
	    deviceId: number;
	    name: string;
	    host: string;
	    checkpoints: number;
	    otherInterfaces: number;
	    locked: string;
	
	    static createFrom(source: any = {}) {
	        return new ManagedDevice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.name = source["name"];
	        this.host = source["host"];
	        this.checkpoints = source["checkpoints"];
	        this.otherInterfaces = source["otherInterfaces"];
	        this.locked = source["locked"];
	    }
	}
	export class DeletePreview {
	    id: string;
	    wholeGroup: boolean;
	    groupName: string;
	    devices: ManagedDevice[];
	    checkpoints: number;
	    confirm: string;
	    blockers: string[];
	    warnings: string[];
	    points: DeletePoint[];
	
	    static createFrom(source: any = {}) {
	        return new DeletePreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.wholeGroup = source["wholeGroup"];
	        this.groupName = source["groupName"];
	        this.devices = this.convertValues(source["devices"], ManagedDevice);
	        this.checkpoints = source["checkpoints"];
	        this.confirm = source["confirm"];
	        this.blockers = source["blockers"];
	        this.warnings = source["warnings"];
	        this.points = this.convertValues(source["points"], DeletePoint);
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
	export class DeleteRequest {
	    manageId: string;
	    deviceId: number;
	    wholeGroup: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DeleteRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.manageId = source["manageId"];
	        this.deviceId = source["deviceId"];
	        this.wholeGroup = source["wholeGroup"];
	    }
	}
	export class DeleteResult {
	    id: string;
	    committed: boolean;
	    notified: boolean;
	    pending: boolean;
	    deviceIds: number[];
	    checkpoints: number;
	    groupDeleted: boolean;
	    keysDeleted: number;
	    keysLeft: string[];
	    dbLeft: number;
	    verified: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new DeleteResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.committed = source["committed"];
	        this.notified = source["notified"];
	        this.pending = source["pending"];
	        this.deviceIds = source["deviceIds"];
	        this.checkpoints = source["checkpoints"];
	        this.groupDeleted = source["groupDeleted"];
	        this.keysDeleted = source["keysDeleted"];
	        this.keysLeft = source["keysLeft"];
	        this.dbLeft = source["dbLeft"];
	        this.verified = source["verified"];
	        this.message = source["message"];
	    }
	}
	export class DeviceChange {
	    deviceId: number;
	    oldName: string;
	    newName: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.oldName = source["oldName"];
	        this.newName = source["newName"];
	    }
	}
	export class DeviceEdit {
	    deviceId: number;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceEdit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.name = source["name"];
	    }
	}
	export class DiscoverField {
	    key: string;
	    label: string;
	    value: string;
	    secret: boolean;
	    source: string;
	
	    static createFrom(source: any = {}) {
	        return new DiscoverField(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.value = source["value"];
	        this.secret = source["secret"];
	        this.source = source["source"];
	    }
	}
	export class DiscoverHostsEntry {
	    server: string;
	    hostname: string;
	    name: string;
	    ip: string;
	
	    static createFrom(source: any = {}) {
	        return new DiscoverHostsEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.server = source["server"];
	        this.hostname = source["hostname"];
	        this.name = source["name"];
	        this.ip = source["ip"];
	    }
	}
	export class DiscoverKafkaRow {
	    hostname: string;
	    ip: string;
	    port: number;
	    source: string;
	    reachable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DiscoverKafkaRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostname = source["hostname"];
	        this.ip = source["ip"];
	        this.port = source["port"];
	        this.source = source["source"];
	        this.reachable = source["reachable"];
	    }
	}
	export class DiscoverKafkaOption {
	    listener: string;
	    security: string;
	    interBroker: boolean;
	    rows: DiscoverKafkaRow[];
	    reachable: number;
	    brokers: number;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new DiscoverKafkaOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.listener = source["listener"];
	        this.security = source["security"];
	        this.interBroker = source["interBroker"];
	        this.rows = this.convertValues(source["rows"], DiscoverKafkaRow);
	        this.reachable = source["reachable"];
	        this.brokers = source["brokers"];
	        this.note = source["note"];
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
	
	export class DiscoverServer {
	    name: string;
	    addr: string;
	    hostname: string;
	    via: string;
	    elevated: boolean;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new DiscoverServer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.addr = source["addr"];
	        this.hostname = source["hostname"];
	        this.via = source["via"];
	        this.elevated = source["elevated"];
	        this.error = source["error"];
	    }
	}
	export class DiscoverResult {
	    fields: DiscoverField[];
	    kafka: DiscoverKafkaOption[];
	    kafkaChoice: number;
	    kafkaReason: string;
	    warnings: string[];
	    servers: DiscoverServer[];
	    hosts: DiscoverHostsEntry[];
	
	    static createFrom(source: any = {}) {
	        return new DiscoverResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fields = this.convertValues(source["fields"], DiscoverField);
	        this.kafka = this.convertValues(source["kafka"], DiscoverKafkaOption);
	        this.kafkaChoice = source["kafkaChoice"];
	        this.kafkaReason = source["kafkaReason"];
	        this.warnings = source["warnings"];
	        this.servers = this.convertValues(source["servers"], DiscoverServer);
	        this.hosts = this.convertValues(source["hosts"], DiscoverHostsEntry);
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
	
	export class ExistingDevice {
	    deviceId: number;
	    interfaceId: number;
	    name: string;
	    host: string;
	    points: number;
	
	    static createFrom(source: any = {}) {
	        return new ExistingDevice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.interfaceId = source["interfaceId"];
	        this.name = source["name"];
	        this.host = source["host"];
	        this.points = source["points"];
	    }
	}
	export class ExistingGroup {
	    groupId: number;
	    name: string;
	    devices: ExistingDevice[];
	    points: number;
	    clusterId: number;
	    vanished: number;
	
	    static createFrom(source: any = {}) {
	        return new ExistingGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.groupId = source["groupId"];
	        this.name = source["name"];
	        this.devices = this.convertValues(source["devices"], ExistingDevice);
	        this.points = source["points"];
	        this.clusterId = source["clusterId"];
	        this.vanished = source["vanished"];
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
	export class KafkaRow {
	    hostname: string;
	    ip: string;
	    port: number;
	
	    static createFrom(source: any = {}) {
	        return new KafkaRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostname = source["hostname"];
	        this.ip = source["ip"];
	        this.port = source["port"];
	    }
	}
	export class PointOverride {
	    checkpointId: number;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new PointOverride(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checkpointId = source["checkpointId"];
	        this.name = source["name"];
	    }
	}
	export class RowEdit {
	    shape: string;
	    name: string;
	    interval: number;
	    overrides: PointOverride[];
	
	    static createFrom(source: any = {}) {
	        return new RowEdit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.shape = source["shape"];
	        this.name = source["name"];
	        this.interval = source["interval"];
	        this.overrides = this.convertValues(source["overrides"], PointOverride);
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
	export class ManageEdit {
	    manageId: string;
	    rows: RowEdit[];
	    devices: DeviceEdit[];
	
	    static createFrom(source: any = {}) {
	        return new ManageEdit(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.manageId = source["manageId"];
	        this.rows = this.convertValues(source["rows"], RowEdit);
	        this.devices = this.convertValues(source["devices"], DeviceEdit);
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
	export class ManageResult {
	    id: string;
	    committed: boolean;
	    notified: boolean;
	    pending: boolean;
	    updated: number[];
	    renamed: number[];
	    conflicts: string[];
	    watched: number;
	    collected: number;
	    missing: number[];
	    verified: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new ManageResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.committed = source["committed"];
	        this.notified = source["notified"];
	        this.pending = source["pending"];
	        this.updated = source["updated"];
	        this.renamed = source["renamed"];
	        this.conflicts = source["conflicts"];
	        this.watched = source["watched"];
	        this.collected = source["collected"];
	        this.missing = source["missing"];
	        this.verified = source["verified"];
	        this.message = source["message"];
	    }
	}
	export class ManageOutcome {
	    kind: string;
	    rename: ManageResult;
	    delete: DeleteResult;
	
	    static createFrom(source: any = {}) {
	        return new ManageOutcome(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.rename = this.convertValues(source["rename"], ManageResult);
	        this.delete = this.convertValues(source["delete"], DeleteResult);
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
	export class PointChange {
	    checkpointId: number;
	    deviceId: number;
	    interfaceId: number;
	    deviceName: string;
	    key: string;
	    shape: string;
	    oldName: string;
	    newName: string;
	    oldInterval: number;
	    newInterval: number;
	    suspicious: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PointChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checkpointId = source["checkpointId"];
	        this.deviceId = source["deviceId"];
	        this.interfaceId = source["interfaceId"];
	        this.deviceName = source["deviceName"];
	        this.key = source["key"];
	        this.shape = source["shape"];
	        this.oldName = source["oldName"];
	        this.newName = source["newName"];
	        this.oldInterval = source["oldInterval"];
	        this.newInterval = source["newInterval"];
	        this.suspicious = source["suspicious"];
	    }
	}
	export class ManagePreview {
	    id: string;
	    points: PointChange[];
	    devices: DeviceChange[];
	    renamed: number;
	    intervals: number;
	    touched: number;
	    interfaces: number;
	    warnings: string[];
	    errors: string[];
	
	    static createFrom(source: any = {}) {
	        return new ManagePreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.points = this.convertValues(source["points"], PointChange);
	        this.devices = this.convertValues(source["devices"], DeviceChange);
	        this.renamed = source["renamed"];
	        this.intervals = source["intervals"];
	        this.touched = source["touched"];
	        this.interfaces = source["interfaces"];
	        this.warnings = source["warnings"];
	        this.errors = source["errors"];
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
	
	
	export class ManagedPoint {
	    checkpointId: number;
	    deviceId: number;
	    interfaceId: number;
	    key: string;
	    name: string;
	    interval: number;
	    templateId: number;
	    format: number;
	    measure: string;
	    host: string;
	    rawName: boolean;
	    serverPart: string;
	
	    static createFrom(source: any = {}) {
	        return new ManagedPoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checkpointId = source["checkpointId"];
	        this.deviceId = source["deviceId"];
	        this.interfaceId = source["interfaceId"];
	        this.key = source["key"];
	        this.name = source["name"];
	        this.interval = source["interval"];
	        this.templateId = source["templateId"];
	        this.format = source["format"];
	        this.measure = source["measure"];
	        this.host = source["host"];
	        this.rawName = source["rawName"];
	        this.serverPart = source["serverPart"];
	    }
	}
	export class ManagedRow {
	    shape: string;
	    template: string;
	    templateWarning: string;
	    points: ManagedPoint[];
	    devices: number;
	    rawName: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ManagedRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.shape = source["shape"];
	        this.template = source["template"];
	        this.templateWarning = source["templateWarning"];
	        this.points = this.convertValues(source["points"], ManagedPoint);
	        this.devices = source["devices"];
	        this.rawName = source["rawName"];
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
	export class ManagedGroup {
	    id: string;
	    groupId: number;
	    name: string;
	    devices: ManagedDevice[];
	    rows: ManagedRow[];
	    checkpoints: number;
	    rawNames: number;
	    otherDevices: string[];
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new ManagedGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.groupId = source["groupId"];
	        this.name = source["name"];
	        this.devices = this.convertValues(source["devices"], ManagedDevice);
	        this.rows = this.convertValues(source["rows"], ManagedRow);
	        this.checkpoints = source["checkpoints"];
	        this.rawNames = source["rawNames"];
	        this.otherDevices = source["otherDevices"];
	        this.warnings = source["warnings"];
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
	
	
	export class Option {
	    id: number;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new Option(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	    }
	}
	export class OrphanDevice {
	    deviceId: number;
	    name: string;
	    checkpoints: number;
	    locked: string;
	
	    static createFrom(source: any = {}) {
	        return new OrphanDevice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deviceId = source["deviceId"];
	        this.name = source["name"];
	        this.checkpoints = source["checkpoints"];
	        this.locked = source["locked"];
	    }
	}
	export class PatternChange {
	    shape: string;
	    field: string;
	    from: string;
	    to: string;
	
	    static createFrom(source: any = {}) {
	        return new PatternChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.shape = source["shape"];
	        this.field = source["field"];
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class PatternEvent {
	    at: string;
	    source: string;
	    notes?: string[];
	    changes: PatternChange[];
	
	    static createFrom(source: any = {}) {
	        return new PatternEvent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = source["at"];
	        this.source = source["source"];
	        this.notes = source["notes"];
	        this.changes = this.convertValues(source["changes"], PatternChange);
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
	export class PendingManage {
	    id: string;
	    kind: string;
	    at: string;
	    groupName: string;
	    committed: boolean;
	    sent: number;
	    total: number;
	    keysLeft: number;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingManage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.at = source["at"];
	        this.groupName = source["groupName"];
	        this.committed = source["committed"];
	        this.sent = source["sent"];
	        this.total = source["total"];
	        this.keysLeft = source["keysLeft"];
	        this.message = source["message"];
	    }
	}
	export class PlacementHint {
	    token: string;
	    host: string;
	    source: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new PlacementHint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token = source["token"];
	        this.host = source["host"];
	        this.source = source["source"];
	        this.note = source["note"];
	    }
	}
	export class Point {
	    key: string;
	    name: string;
	    host: string;
	    templateId: number;
	    format: number;
	    measure: string;
	    interval: number;
	    warning: string;
	    selected: boolean;
	    remembered: boolean;
	    registered: boolean;
	    registeredOn: string;
	    moved: boolean;
	    deleted: boolean;
	    checkpointId: number;
	    fromInterfaceId: number;
	    moveGroupId: number;
	    move: boolean;
	    badValue: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Point(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.name = source["name"];
	        this.host = source["host"];
	        this.templateId = source["templateId"];
	        this.format = source["format"];
	        this.measure = source["measure"];
	        this.interval = source["interval"];
	        this.warning = source["warning"];
	        this.selected = source["selected"];
	        this.remembered = source["remembered"];
	        this.registered = source["registered"];
	        this.registeredOn = source["registeredOn"];
	        this.moved = source["moved"];
	        this.deleted = source["deleted"];
	        this.checkpointId = source["checkpointId"];
	        this.fromInterfaceId = source["fromInterfaceId"];
	        this.moveGroupId = source["moveGroupId"];
	        this.move = source["move"];
	        this.badValue = source["badValue"];
	    }
	}
	export class Server {
	    hostname: string;
	    name: string;
	    ip: string;
	
	    static createFrom(source: any = {}) {
	        return new Server(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostname = source["hostname"];
	        this.name = source["name"];
	        this.ip = source["ip"];
	    }
	}
	export class Plan {
	    id: string;
	    servers: Server[];
	    points: Point[];
	    explorers: Option[];
	    clusters: Option[];
	    groupName: string;
	    warnings: string[];
	    redisInfo: string;
	    redisMaster: string;
	    orphans: number;
	    patternInfo: string;
	    existing: ExistingGroup[];
	
	    static createFrom(source: any = {}) {
	        return new Plan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.servers = this.convertValues(source["servers"], Server);
	        this.points = this.convertValues(source["points"], Point);
	        this.explorers = this.convertValues(source["explorers"], Option);
	        this.clusters = this.convertValues(source["clusters"], Option);
	        this.groupName = source["groupName"];
	        this.warnings = source["warnings"];
	        this.redisInfo = source["redisInfo"];
	        this.redisMaster = source["redisMaster"];
	        this.orphans = source["orphans"];
	        this.patternInfo = source["patternInfo"];
	        this.existing = this.convertValues(source["existing"], ExistingGroup);
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
	
	
	
	export class RegistrationRecord {
	    id: string;
	    groupName: string;
	    at: string;
	    db: string;
	    stage: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new RegistrationRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.groupName = source["groupName"];
	        this.at = source["at"];
	        this.db = source["db"];
	        this.stage = source["stage"];
	        this.message = source["message"];
	    }
	}
	export class ResolvedName {
	    key: string;
	    host: string;
	    from: string;
	    to: string;
	
	    static createFrom(source: any = {}) {
	        return new ResolvedName(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.host = source["host"];
	        this.from = source["from"];
	        this.to = source["to"];
	    }
	}
	export class Result {
	    id: string;
	    groupId: number;
	    deviceIds: number[];
	    interfaceIds: number[];
	    checkpointIds: number[];
	    movedIds: number[];
	    newDeviceIds: number[];
	    update: boolean;
	    stage: string;
	    committed: boolean;
	    notified: boolean;
	    verified: boolean;
	    collected: number;
	    missing: number[];
	    message: string;
	    names: ResolvedName[];
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.groupId = source["groupId"];
	        this.deviceIds = source["deviceIds"];
	        this.interfaceIds = source["interfaceIds"];
	        this.checkpointIds = source["checkpointIds"];
	        this.movedIds = source["movedIds"];
	        this.newDeviceIds = source["newDeviceIds"];
	        this.update = source["update"];
	        this.stage = source["stage"];
	        this.committed = source["committed"];
	        this.notified = source["notified"];
	        this.verified = source["verified"];
	        this.collected = source["collected"];
	        this.missing = source["missing"];
	        this.message = source["message"];
	        this.names = this.convertValues(source["names"], ResolvedName);
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
	
	export class SavedConnection {
	    config: Config;
	    kafkaRows: KafkaRow[];
	    savedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new SavedConnection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.config = this.convertValues(source["config"], Config);
	        this.kafkaRows = this.convertValues(source["kafkaRows"], KafkaRow);
	        this.savedAt = source["savedAt"];
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
	export class Selection {
	    planId: string;
	    groupName: string;
	    explorerId: number;
	    clusterId: number;
	    servers: Server[];
	    points: Point[];
	    updateGroupId: number;
	
	    static createFrom(source: any = {}) {
	        return new Selection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.planId = source["planId"];
	        this.groupName = source["groupName"];
	        this.explorerId = source["explorerId"];
	        this.clusterId = source["clusterId"];
	        this.servers = this.convertValues(source["servers"], Server);
	        this.points = this.convertValues(source["points"], Point);
	        this.updateGroupId = source["updateGroupId"];
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


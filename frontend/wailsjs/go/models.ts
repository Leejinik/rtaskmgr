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
	
	    static createFrom(source: any = {}) {
	        return new LogMeta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.hosts = this.convertValues(source["hosts"], LogHostInfo);
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
	
	export class RecTarget {
	    path: string;
	    mount: string;
	    totalBytes: number;
	    freeBytes: number;
	    writable: boolean;
	    needsSudo: boolean;
	
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

